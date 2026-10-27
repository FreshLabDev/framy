// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// inAlbum is a message of an album: Telegram gives each item its own message
// and the same media_group_id.
func inAlbum(chat tg.Chat, id int64, group string) *tg.Message {
	return &tg.Message{
		MessageID: id, Chat: chat, From: &tg.User{ID: 9}, Date: 1_790_000_000, MediaGroupID: group,
	}
}

func photoMessage(chat tg.Chat, id int64, group string) *tg.Message {
	m := inAlbum(chat, id, group)
	m.Photo = []tg.Photo{
		{FileID: fmt.Sprintf("small-%d", id), FileUniqueID: fmt.Sprintf("u-small-%d", id), Width: 90, Height: 67, FileSize: 1_000},
		{FileID: fmt.Sprintf("large-%d", id), FileUniqueID: fmt.Sprintf("u-large-%d", id), Width: 1280, Height: 960, FileSize: 150_000},
		{FileID: fmt.Sprintf("medium-%d", id), FileUniqueID: fmt.Sprintf("u-medium-%d", id), Width: 320, Height: 240, FileSize: 12_000},
	}
	return m
}

// Which item an album message gives, and which messages give none.
func TestAlbumItemIsPickedFromTheMessage(t *testing.T) {
	still := []tg.Photo{
		{FileID: "still-small", FileUniqueID: "u-still-small", Width: 90, Height: 120},
		{FileID: "still-large", FileUniqueID: "u-still-large", Width: 960, Height: 1280, FileSize: 90_000},
	}
	for name, tc := range map[string]struct {
		message func() *tg.Message
		want    *db.AlbumItem // nil: not an item
	}{
		"the largest size of a photo": {
			func() *tg.Message { return photoMessage(groupChat, 11, "g") },
			&db.AlbumItem{Kind: db.AlbumPhoto, FileID: "large-11", FileUniqueID: "u-large-11", Width: 1280, Height: 960, FileSize: 150_000},
		},
		"a live photo is its still frame, not its video": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.LivePhoto = &tg.LivePhoto{FileID: "the-video", FileUniqueID: "u-the-video", Photo: still, Duration: 3}
				m.Photo = still
				return m
			},
			&db.AlbumItem{Kind: db.AlbumPhoto, FileID: "still-large", FileUniqueID: "u-still-large", Width: 960, Height: 1280, FileSize: 90_000},
		},
		"a live photo whose stills are only in photo": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.LivePhoto = &tg.LivePhoto{FileID: "the-video", FileUniqueID: "u-the-video"}
				m.Photo = still
				return m
			},
			&db.AlbumItem{Kind: db.AlbumPhoto, FileID: "still-large", FileUniqueID: "u-still-large", Width: 960, Height: 1280, FileSize: 90_000},
		},
		"a live photo with no still frame": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.LivePhoto = &tg.LivePhoto{FileID: "the-video", FileUniqueID: "u-the-video"}
				return m
			},
			nil,
		},
		"a video": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Video = &tg.Video{FileID: "vid", FileUniqueID: "u-vid", Width: 1920, Height: 1080, Duration: 4, FileSize: 2_000_000}
				return m
			},
			&db.AlbumItem{Kind: db.AlbumVideo, FileID: "vid", FileUniqueID: "u-vid", Width: 1920, Height: 1080, FileSize: 2_000_000},
		},
		"an image document": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Document = &tg.Document{FileID: "doc", FileUniqueID: "u-doc", MimeType: "image/png", FileSize: 800_000}
				return m
			},
			&db.AlbumItem{Kind: db.AlbumDocument, FileID: "doc", FileUniqueID: "u-doc", FileSize: 800_000},
		},
		"a video document, whatever the case of its type": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Document = &tg.Document{FileID: "doc", FileUniqueID: "u-doc", MimeType: "Video/MP4"}
				return m
			},
			&db.AlbumItem{Kind: db.AlbumDocument, FileID: "doc", FileUniqueID: "u-doc"},
		},
		"a document that is neither": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Document = &tg.Document{FileID: "doc", FileUniqueID: "u-doc", MimeType: "application/pdf"}
				return m
			},
			nil,
		},
		"a document with no type": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Document = &tg.Document{FileID: "doc", FileUniqueID: "u-doc"}
				return m
			},
			nil,
		},
		"a GIF, which Telegram also fills as a document": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Animation = &tg.Animation{FileID: "gif", FileUniqueID: "u-gif", MimeType: "video/mp4"}
				m.Document = &tg.Document{FileID: "gif", FileUniqueID: "u-gif", MimeType: "video/mp4"}
				return m
			},
			nil,
		},
		"a sticker": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Sticker = &tg.Sticker{FileID: "st", FileUniqueID: "u-st"}
				return m
			},
			nil,
		},
		"a text": {
			func() *tg.Message { m := inAlbum(groupChat, 11, "g"); m.Text = "hello"; return m },
			nil,
		},
		"a photo with no media group": {
			func() *tg.Message { return photoMessage(groupChat, 11, "") },
			nil,
		},
		"a photo whose sizes have no file": {
			func() *tg.Message {
				m := inAlbum(groupChat, 11, "g")
				m.Photo = []tg.Photo{{Width: 90, Height: 67}}
				return m
			},
			nil,
		},
		"a post in a channel": {
			func() *tg.Message { return photoMessage(tg.Chat{ID: -1002, Type: "channel"}, 11, "g") },
			nil,
		},
		"a photo with no sender": {
			func() *tg.Message { m := photoMessage(groupChat, 11, "g"); m.From = nil; return m },
			&db.AlbumItem{Kind: db.AlbumPhoto, FileID: "large-11", FileUniqueID: "u-large-11", Width: 1280, Height: 960, FileSize: 150_000},
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := tc.message()
			got, ok := albumItem(m)
			if tc.want == nil {
				if ok {
					t.Fatalf("a message with nothing to keep gave %+v", got)
				}
				return
			}
			if !ok {
				t.Fatal("no item")
			}
			want := *tc.want
			want.ChatID, want.MediaGroupID, want.MessageID = m.Chat.ID, "g", 11
			want.SentAt = time.Unix(1_790_000_000, 0).UTC()
			if m.From != nil {
				want.UserID = 9
			}
			if got != want {
				t.Errorf("item = %+v\nwant   %+v", got, want)
			}
		})
	}
}

// The three chats an album is kept in; every other type is left alone.
func TestAlbumIsKeptInDirectChatsGroupsAndSupergroups(t *testing.T) {
	for typ, kept := range map[string]bool{"private": true, "group": true, "supergroup": true, "channel": false, "sender": false, "": false} {
		if _, ok := albumItem(photoMessage(tg.Chat{ID: -5, Type: typ}, 1, "g")); ok != kept {
			t.Errorf("%q: kept = %v", typ, ok)
		}
	}
}

func counted(t *testing.T) int {
	t.Helper()
	var out bytes.Buffer
	metrics.Write(&out)
	for _, line := range strings.Split(out.String(), "\n") {
		var n int
		if _, err := fmt.Sscanf(line, "framy_album_note_failures_total %d", &n); err == nil {
			return n
		}
	}
	t.Fatal("framy_album_note_failures_total is not exposed")
	return 0
}

// An item is kept whatever else the message is, and a command is still
// answered; the text is never part of what is kept.
func TestEveryAlbumMessageIsNotedCommandOrNot(t *testing.T) {
	b, store, client := newBot()
	ctx := context.Background()
	first := photoMessage(groupChat, 21, "g1")
	first.Caption = "/img make it a painting"
	second := photoMessage(groupChat, 22, "g1")
	second.Caption = "my holiday"
	third := photoMessage(dmChat, 23, "g2")
	start := inAlbum(dmChat, 24, "g3")
	start.Text = "/start"
	for i, m := range []*tg.Message{first, second, third, start} {
		b.handle(ctx, tg.Update{UpdateID: int64(i), Message: m})
	}
	if store.notes != 3 {
		t.Fatalf("%d items noted, want 3: %+v", store.notes, store.items)
	}
	if it := store.items[albumID{-100, 21}]; it.FileID != "large-21" || it.MediaGroupID != "g1" || it.UserID != 9 {
		t.Errorf("the command's photo was noted as %+v", it)
	}
	if it := store.items[albumID{7, 23}]; it.FileID != "large-23" {
		t.Errorf("the DM's photo was noted as %+v", it)
	}
	// Both senders were still touched, and /start still got its panel.
	if len(store.touches) != 4 {
		t.Errorf("touches = %+v", store.touches)
	}
	if client.last().method != "sendMessage" || client.last().chatID != 7 {
		t.Errorf("/start was answered with %+v", client.last())
	}
}

// An edit can swap the file of an item. It is noted and nothing else is done
// for it: no touch, no command.
func TestAnEditedAlbumMessageIsNotedAndNothingElse(t *testing.T) {
	b, store, client := newBot()
	m := photoMessage(groupChat, 31, "g")
	m.Caption = "/start"
	b.handle(context.Background(), tg.Update{UpdateID: 1, Message: m})
	swapped := photoMessage(groupChat, 31, "g")
	swapped.Photo = []tg.Photo{{FileID: "swapped", FileUniqueID: "u-swapped", Width: 800, Height: 600}}
	swapped.Caption = "/start"
	before := len(store.touches)
	b.handle(context.Background(), tg.Update{UpdateID: 2, EditedMessage: swapped})
	if len(store.items) != 1 || store.items[albumID{-100, 31}].FileID != "swapped" {
		t.Errorf("items = %+v", store.items)
	}
	if len(store.touches) != before || len(client.calls) != 0 {
		t.Errorf("an edit touched %d more and sent %+v", len(store.touches)-before, client.calls)
	}
}

// A message that looks like a key is deleted and nothing of it is kept, the
// photo's ids included. The same in the group is left alone: only a DM is
// guarded.
func TestAKeyMessageInADMIsNotNoted(t *testing.T) {
	b, store, client := newBot()
	ctx := context.Background()
	dm := photoMessage(dmChat, 41, "g")
	dm.Caption = "key " + openAIKey
	b.handle(ctx, tg.Update{UpdateID: 1, Message: dm})
	edited := photoMessage(dmChat, 42, "g")
	edited.Caption = "key " + openAIKey
	b.handle(ctx, tg.Update{UpdateID: 2, EditedMessage: edited})
	if len(store.items) != 0 {
		t.Errorf("a key's message was noted: %+v", store.items)
	}
	if len(client.calls) != 2 || client.calls[0].method != "deleteMessage" {
		t.Errorf("calls = %+v", client.calls)
	}
	group := photoMessage(groupChat, 43, "g")
	group.Caption = "key " + openAIKey
	b.handle(ctx, tg.Update{UpdateID: 3, Message: group})
	if len(store.items) != 1 {
		t.Errorf("a group's photo was not noted: %+v", store.items)
	}
}

// A store that fails costs the album an item, not the update: the sender is
// still touched, the command still answered, and the failure is logged and
// counted.
func TestAFailedNoteStopsNothing(t *testing.T) {
	var logs bytes.Buffer
	store, client := newStore(), newTelegram()
	b := New(store, client, slog.New(slog.NewTextHandler(&logs, nil)), "framytest_bot", selfID, "v0.1.0")
	store.noteErr = errors.New("connection reset")
	// Telegram sends no text with a photo; the handler must not care, and this
	// is the shortest way to see it go on to the command.
	m := photoMessage(dmChat, 51, "g")
	m.Text = "/start"
	before := counted(t)
	b.handle(context.Background(), tg.Update{UpdateID: 1, Message: m})
	if len(store.touches) != 1 || client.last().method != "sendMessage" {
		t.Errorf("touches = %+v, last call %+v", store.touches, client.last())
	}
	if got := counted(t); got != before+1 {
		t.Errorf("the failure count went from %d to %d", before, got)
	}
	if !strings.Contains(logs.String(), "album item not saved") || !strings.Contains(logs.String(), "connection reset") {
		t.Errorf("the failure was not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "large-51") {
		t.Error("the log holds a file id")
	}
}

// A wait ends when the album has been quiet for a moment, and whatever came
// meanwhile is in it.
func TestAlbumWaitsForTheRest(t *testing.T) {
	b, _, _ := newBot()
	ctx := context.Background()
	b.noteAlbumItem(ctx, photoMessage(groupChat, 61, "g"))

	const settle = 150 * time.Millisecond
	go func() {
		for _, id := range []int64{62, 63} {
			time.Sleep(50 * time.Millisecond)
			b.noteAlbumItem(ctx, photoMessage(groupChat, id, "g"))
		}
	}()
	start := time.Now()
	items, err := b.Album(ctx, groupChat.ID, "g", settle, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].MessageID != 61 || items[1].MessageID != 62 || items[2].MessageID != 63 {
		t.Fatalf("album = %+v", items)
	}
	// The last item came 100 ms in, and the album is read one settle later.
	if took := time.Since(start); took < 100*time.Millisecond+settle-20*time.Millisecond || took > 3*time.Second {
		t.Errorf("waited %v", took)
	}
}

func TestAlbumThatStoppedGrowingIsReadAtOnce(t *testing.T) {
	b, store, _ := newBot()
	ctx := context.Background()
	b.noteAlbumItem(ctx, photoMessage(groupChat, 71, "g"))
	b.arrivals.mark(albumKey{-100, "g"}, time.Now().Add(-time.Second))
	start := time.Now()
	items, err := b.Album(ctx, -100, "g", 800*time.Millisecond, 5*time.Second)
	if err != nil || len(items) != 1 || items[0].MessageID != 71 {
		t.Fatalf("album = %+v, %v", items, err)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("waited %v for an album that was quiet already", took)
	}

	// An album the bot has not seen grow in the last minute has no record to
	// wait on, and what the store has is what it returns.
	store.items[albumID{-100, 99}] = db.AlbumItem{ChatID: -100, MediaGroupID: "old", MessageID: 99, CreatedAt: time.Now()}
	start = time.Now()
	items, err = b.Album(ctx, -100, "old", 800*time.Millisecond, 5*time.Second)
	if err != nil || len(items) != 1 || time.Since(start) > 400*time.Millisecond {
		t.Errorf("an unseen album: %+v, %v after %v", items, err, time.Since(start))
	}
	// Another chat's album with the same id is another album.
	if items, err = b.Album(ctx, -200, "g", time.Second, time.Second); err != nil || len(items) != 0 {
		t.Errorf("another chat's album: %+v, %v", items, err)
	}
}

// An album that keeps growing is read at the cap, not when it ends.
func TestAlbumWaitIsCapped(t *testing.T) {
	b, _, _ := newBot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.noteAlbumItem(ctx, photoMessage(groupChat, 81, "g"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for id := int64(82); ctx.Err() == nil; id++ {
			time.Sleep(10 * time.Millisecond)
			b.noteAlbumItem(ctx, photoMessage(groupChat, id, "g"))
		}
	}()
	start := time.Now()
	items, err := b.Album(ctx, -100, "g", 300*time.Millisecond, 150*time.Millisecond)
	took := time.Since(start)
	cancel()
	<-done
	if err != nil || len(items) < 2 {
		t.Fatalf("album = %d items, %v", len(items), err)
	}
	if took < 140*time.Millisecond || took > 280*time.Millisecond {
		t.Errorf("waited %v, the cap was 150ms", took)
	}
}

func TestAlbumWaitStopsWithTheContext(t *testing.T) {
	b, _, _ := newBot()
	b.noteAlbumItem(context.Background(), photoMessage(groupChat, 91, "g"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	items, err := b.Album(ctx, -100, "g", 10*time.Second, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || items != nil {
		t.Errorf("album = %+v, %v", items, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("waited %v after the context ended", took)
	}
}

// The record of arrivals is in memory and bounded: an album nothing came to
// for a minute is forgotten, sooner or later by the next arrival.
func TestArrivalsAreForgottenAfterAMinute(t *testing.T) {
	a := newArrivals()
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	one, two, three := albumKey{1, "a"}, albumKey{2, "b"}, albumKey{3, "c"}
	a.mark(one, t0)
	a.mark(two, t0.Add(30*time.Second))
	if !a.at(one).Equal(t0) || !a.at(two).Equal(t0.Add(30*time.Second)) {
		t.Fatalf("one: %v, two: %v", a.at(one), a.at(two))
	}
	a.mark(three, t0.Add(61*time.Second))
	if !a.at(one).IsZero() {
		t.Errorf("an album quiet for 61 s is still remembered: %v", a.at(one))
	}
	if a.at(two).IsZero() || a.at(three).IsZero() {
		t.Error("a recent album was forgotten")
	}
	if len(a.last) != 2 {
		t.Errorf("the record holds %d albums", len(a.last))
	}
	// Marks that come faster than the sweep do not pay for it each time.
	a.mark(one, t0.Add(62*time.Second))
	a.mark(two, t0.Add(63*time.Second))
	if len(a.last) != 3 {
		t.Errorf("the record holds %d albums", len(a.last))
	}
}

// SeesEveryMessage: a direct chat always, a group while framy is an admin or
// the creator, and Telegram is asked every time.
func TestSeesEveryMessage(t *testing.T) {
	ctx := context.Background()
	b, _, client := newBot()
	got, err := b.SeesEveryMessage(ctx, 7)
	if err != nil || !got {
		t.Fatalf("a direct chat: %v, %v", got, err)
	}
	if client.members != 0 {
		t.Errorf("a direct chat asked Telegram %d times", client.members)
	}
	for status, want := range map[string]bool{
		"administrator": true, "creator": true,
		"member": false, "restricted": false, "left": false, "kicked": false,
	} {
		client.statuses[selfID] = status
		got, err := b.SeesEveryMessage(ctx, -1001234)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v; want %v", status, got, err, want)
		}
	}
	// Someone else's rights say nothing about framy's.
	client.statuses = map[int64]string{9: "administrator"}
	if got, _ := b.SeesEveryMessage(ctx, -1001234); got {
		t.Error("another member's admin rights were taken for framy's")
	}
}

func TestSeesEveryMessageIsAskedEveryTime(t *testing.T) {
	ctx := context.Background()
	b, _, client := newBot()
	client.statuses[selfID] = "administrator"
	if got, _ := b.SeesEveryMessage(ctx, -100); !got {
		t.Fatal("not seen as an admin")
	}
	client.statuses[selfID] = "member"
	if got, _ := b.SeesEveryMessage(ctx, -100); got {
		t.Error("an answer outlived the rights it was for")
	}
	if client.members != 2 {
		t.Errorf("Telegram was asked %d times, want 2", client.members)
	}
	client.memberErr = errors.New("Bad Request: chat not found")
	if got, err := b.SeesEveryMessage(ctx, -100); got || err == nil {
		t.Errorf("a failed check: %v, %v", got, err)
	}
}

// The clean-up runs at start with a cut-off 48 hours back, then every
// interval, and ends with its context.
func TestAlbumCacheIsSweptHourly(t *testing.T) {
	var logs bytes.Buffer
	store, client := newStore(), newTelegram()
	b := New(store, client, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), "framytest_bot", selfID, "v0.1.0")
	now := time.Now()
	store.items[albumID{-100, 1}] = db.AlbumItem{ChatID: -100, MediaGroupID: "g", MessageID: 1, CreatedAt: now.Add(-49 * time.Hour)}
	store.items[albumID{-100, 2}] = db.AlbumItem{ChatID: -100, MediaGroupID: "g", MessageID: 2, CreatedAt: now.Add(-47 * time.Hour)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.sweepAlbums(ctx, 5*time.Millisecond); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		store.mu.Lock()
		sweeps := len(store.swept)
		store.mu.Unlock()
		if sweeps >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d sweeps", sweeps)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not stop with its context")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, old := store.items[albumID{-100, 1}]; old {
		t.Error("an item of 49 hours was kept")
	}
	if _, fresh := store.items[albumID{-100, 2}]; !fresh {
		t.Error("an item of 47 hours was deleted")
	}
	if cut := store.swept[0]; cut.Before(now.Add(-48*time.Hour-time.Minute)) || cut.After(now.Add(-48*time.Hour+time.Minute)) {
		t.Errorf("the cut-off was %v, not 48 hours back", cut)
	}
	if !strings.Contains(logs.String(), "level=DEBUG") || !strings.Contains(logs.String(), `msg="album cache swept" deleted=1`) {
		t.Errorf("the count was not logged at debug: %s", logs.String())
	}
}

// Run starts the clean-up beside polling and waits for it before it returns.
func TestRunSweepsTheAlbumCacheAndWaitsForIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, client := newStore(), newTelegram()
	client.cancel = cancel
	store.items[albumID{-100, 1}] = db.AlbumItem{ChatID: -100, MediaGroupID: "g", MessageID: 1, CreatedAt: time.Now().Add(-72 * time.Hour)}
	b := New(store, client, quiet(), "framytest_bot", selfID, "v0.1.0")
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.swept) != 1 || len(store.items) != 0 {
		t.Errorf("sweeps = %v, items = %+v", store.swept, store.items)
	}
}
