// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
)

// selfID is the id the test bot has in Telegram.
const selfID = int64(555)

type touch struct {
	userID int64
	chatID int64
}

type fakeStore struct {
	mu       sync.Mutex
	offset   int64
	touches  []touch
	userLang map[int64]string
	chatLang map[int64]string
	// fallback is what core answers for a person once framy's claim is gone.
	fallback map[int64]string

	// items is the album cache by chat and message; noteErr makes a save fail;
	// notes counts the saves and swept holds the cut-off of every clean-up.
	items   map[albumID]db.AlbumItem
	noteErr error
	notes   int
	swept   []time.Time
}

type albumID struct{ chat, message int64 }

func newStore() *fakeStore {
	return &fakeStore{
		userLang: map[int64]string{}, chatLang: map[int64]string{}, fallback: map[int64]string{},
		items: map[albumID]db.AlbumItem{},
	}
}

func (f *fakeStore) NoteAlbumItem(_ context.Context, it db.AlbumItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noteErr != nil {
		return f.noteErr
	}
	f.notes++
	id := albumID{it.ChatID, it.MessageID}
	if old, ok := f.items[id]; ok {
		it.CreatedAt = old.CreatedAt
	} else if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now()
	}
	f.items[id] = it
	return nil
}

func (f *fakeStore) AlbumItems(_ context.Context, chatID int64, group string) ([]db.AlbumItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.AlbumItem
	for _, it := range f.items {
		if it.ChatID == chatID && it.MediaGroupID == group {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(a, b db.AlbumItem) int { return int(a.MessageID - b.MessageID) })
	return out, nil
}

func (f *fakeStore) DeleteAlbumItemsBefore(_ context.Context, t time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swept = append(f.swept, t)
	var n int64
	for id, it := range f.items {
		if it.CreatedAt.Before(t) {
			delete(f.items, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) Offset(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.offset, nil
}

func (f *fakeStore) AdvanceOffset(_ context.Context, offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offset = max(f.offset, offset)
	return nil
}

func (f *fakeStore) Touch(_ context.Context, user tg.User, chat *tg.Chat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var chatID int64
	if chat != nil {
		chatID = chat.ID
	}
	f.touches = append(f.touches, touch{user.ID, chatID})
	return nil
}

func (f *fakeStore) UserLang(_ context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.userLang[id], nil
}

func (f *fakeStore) ChatLang(_ context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatLang[id], nil
}

func (f *fakeStore) SetUserLang(_ context.Context, id int64, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userLang[id] = lang
	return nil
}

func (f *fakeStore) ClearUserLang(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userLang[id] = f.fallback[id]
	return nil
}

func (f *fakeStore) SetChatLang(_ context.Context, id int64, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatLang[id] = lang
	return nil
}

func (f *fakeStore) ClearChatLang(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.chatLang, id)
	return nil
}

// sent is one call that changed something in a chat.
type sent struct {
	method   string
	chatID   int64
	receiver int64
	id       int64
	text     string
	markup   *tg.InlineKeyboardMarkup
}

// fakeTelegram hands out one batch, then cancels the run on the next poll,
// and records every call that writes.
type fakeTelegram struct {
	mu       sync.Mutex
	batch    []tg.Update
	offsets  []int64
	cancel   context.CancelFunc
	calls    []sent
	answers  []string
	alerts   []string
	statuses map[int64]string
	commands map[string][]tg.BotCommand
	// profile is what Telegram holds for get/setMy(Short)Description, keyed
	// by method field and language; writes counts the set calls.
	profile map[string]string
	writes  int
	// members counts the GetChatMember calls, and memberErr makes them fail.
	members   int
	memberErr error
}

func newTelegram() *fakeTelegram {
	return &fakeTelegram{statuses: map[int64]string{}, commands: map[string][]tg.BotCommand{}, profile: map[string]string{}}
}

func (f *fakeTelegram) record(s sent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeTelegram) last() sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return sent{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeTelegram) DeleteWebhook(context.Context) error { return nil }

func (f *fakeTelegram) GetUpdates(ctx context.Context, offset int64, _ int) ([]tg.Update, error) {
	f.mu.Lock()
	f.offsets = append(f.offsets, offset)
	first := len(f.offsets) == 1
	f.mu.Unlock()
	if first {
		return f.batch, nil
	}
	f.cancel()
	return nil, ctx.Err()
}

func (f *fakeTelegram) SendMessage(_ context.Context, chatID int64, text string, m *tg.InlineKeyboardMarkup) (tg.Message, error) {
	f.record(sent{method: "sendMessage", chatID: chatID, text: text, markup: m})
	return tg.Message{MessageID: 1}, nil
}

func (f *fakeTelegram) EditMessageText(_ context.Context, chatID, messageID int64, text string, m *tg.InlineKeyboardMarkup) error {
	f.record(sent{method: "editMessageText", chatID: chatID, id: messageID, text: text, markup: m})
	return nil
}

func (f *fakeTelegram) SendEphemeralMessage(_ context.Context, chatID, receiver, ephemeralID int64, text string, m *tg.InlineKeyboardMarkup) (tg.Message, error) {
	f.record(sent{method: "sendEphemeral", chatID: chatID, receiver: receiver, id: ephemeralID, text: text, markup: m})
	return tg.Message{}, nil
}

func (f *fakeTelegram) EditEphemeralMessageText(_ context.Context, chatID, receiver, ephemeralID int64, text string, m *tg.InlineKeyboardMarkup) error {
	f.record(sent{method: "editEphemeral", chatID: chatID, receiver: receiver, id: ephemeralID, text: text, markup: m})
	return nil
}

func (f *fakeTelegram) DeleteEphemeralMessage(_ context.Context, chatID, receiver, ephemeralID int64) error {
	f.record(sent{method: "deleteEphemeral", chatID: chatID, receiver: receiver, id: ephemeralID})
	return nil
}

func (f *fakeTelegram) DeleteMessage(_ context.Context, chatID, messageID int64) error {
	f.record(sent{method: "deleteMessage", chatID: chatID, id: messageID})
	return nil
}

func (f *fakeTelegram) AnswerCallbackQuery(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, id)
	return nil
}

func (f *fakeTelegram) AnswerCallbackQueryAlert(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, text)
	return nil
}

func (f *fakeTelegram) GetChatMember(_ context.Context, _, userID int64) (tg.ChatMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members++
	if f.memberErr != nil {
		return tg.ChatMember{}, f.memberErr
	}
	status := f.statuses[userID]
	if status == "" {
		status = "member"
	}
	return tg.ChatMember{Status: status}, nil
}

func (f *fakeTelegram) SetMyCommandsForScope(_ context.Context, cmds []tg.BotCommand, sc *tg.BotCommandScope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands[sc.Type+"/"+sc.LanguageCode] = cmds
	return nil
}

func (f *fakeTelegram) CallGet(_ context.Context, method string, values url.Values, out any) error {
	field := map[string]string{"getMyDescription": "description", "getMyShortDescription": "short_description"}[method]
	f.mu.Lock()
	text := f.profile[field+"/"+values.Get("language_code")]
	f.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]string{field: text}})
	return json.Unmarshal(body, out)
}

func (f *fakeTelegram) SetMyDescription(_ context.Context, text, lang string) error {
	return f.setProfile("description", text, lang)
}

func (f *fakeTelegram) SetMyShortDescription(_ context.Context, text, lang string) error {
	return f.setProfile("short_description", text, lang)
}

func (f *fakeTelegram) setProfile(field, text, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profile[field+"/"+lang] = text
	f.writes++
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunTouchesSendersAndAdvancesOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := newStore()
	store.offset = 40
	client := newTelegram()
	client.cancel = cancel
	client.batch = []tg.Update{
		{UpdateID: 40, Message: &tg.Message{From: &tg.User{ID: 1}, Chat: tg.Chat{ID: -100, Type: "supergroup"}}},
		{UpdateID: 41, Message: &tg.Message{Chat: tg.Chat{ID: -200, Type: "channel"}}},
		{UpdateID: 42, MyChatMember: &tg.ChatMemberUpdated{From: tg.User{ID: 2}, Chat: tg.Chat{ID: -300, Type: "group"}}},
	}
	b := New(store, client, quiet(), "framytest_bot", selfID, "v0.0.0")

	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.Initialized() || b.LastPoll().IsZero() {
		t.Fatalf("initialized = %v, last poll = %v", b.Initialized(), b.LastPoll())
	}
	if len(client.offsets) != 2 || client.offsets[0] != 40 || client.offsets[1] != 43 {
		t.Fatalf("polled offsets = %v, want [40 43]", client.offsets)
	}
	if store.offset != 43 {
		t.Fatalf("stored offset = %d, want 43", store.offset)
	}
	want := []touch{{1, -100}, {2, -300}}
	if len(store.touches) != len(want) {
		t.Fatalf("touches = %+v, want %+v", store.touches, want)
	}
	for i := range want {
		if store.touches[i] != want[i] {
			t.Fatalf("touches = %+v, want %+v", store.touches, want)
		}
	}
}
