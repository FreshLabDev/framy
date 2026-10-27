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

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// The keys in these tests are made, not written out, so no scanner takes
// the test file for a leak.
var (
	openAIKey     = "sk-proj-" + strings.Repeat("aB3xY9", 12)
	openRouterKey = "sk-or-v1-" + strings.Repeat("0123456789abcdef", 4)
	geminiKey     = "AIza" + strings.Repeat("Xy7_-Qw", 5)
)

// keysInChat reads the counter of deleted key messages from the metrics text.
func keysInChat(t *testing.T) int {
	t.Helper()
	var out bytes.Buffer
	metrics.Write(&out)
	for _, line := range strings.Split(out.String(), "\n") {
		var n int
		if _, err := fmt.Sscanf(line, "framy_keys_in_chat_total %d", &n); err == nil {
			return n
		}
	}
	t.Fatal("framy_keys_in_chat_total is not exposed")
	return 0
}

func TestAKeyInADMIsDeletedAtOnce(t *testing.T) {
	for name, key := range map[string]string{"OpenAI": openAIKey, "OpenRouter": openRouterKey, "Gemini": geminiKey} {
		for shape, text := range map[string]string{
			"alone":      key,
			"in a line":  "here is my key " + key + " please use it",
			"in quotes":  `"` + key + `"`,
			"on a line":  "first line\n" + key,
			"in a flag":  "key=" + key,
			"in a block": "```\n" + key + "\n```",
		} {
			t.Run(name+" "+shape, func(t *testing.T) {
				b, store, client := newBot()
				store.userLang[7] = "uk"
				before := keysInChat(t)
				if err := b.onMessage(context.Background(), message(dmChat, 7, text)); err != nil {
					t.Fatal(err)
				}
				if len(client.calls) != 2 {
					t.Fatalf("calls = %+v", client.calls)
				}
				if del := client.calls[0]; del.method != "deleteMessage" || del.chatID != 7 || del.id != 10 {
					t.Errorf("the first call was %+v, not the delete of message 10", del)
				}
				answer := client.calls[1]
				if answer.method != "sendMessage" || answer.chatID != 7 || answer.text != i18n.T("uk", "msg.key_deleted") {
					t.Errorf("answered %+v", answer)
				}
				if strings.Contains(answer.text, key[:12]) {
					t.Error("the answer repeats the key")
				}
				if got := keysInChat(t); got != before+1 {
					t.Errorf("framy_keys_in_chat_total went from %d to %d", before, got)
				}
			})
		}
	}
}

func TestAKeyInACaptionIsDeleted(t *testing.T) {
	b, _, client := newBot()
	m := message(dmChat, 7, "")
	m.Caption = "my key: " + openAIKey
	if err := b.onMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 || client.calls[0].method != "deleteMessage" {
		t.Fatalf("calls = %+v", client.calls)
	}
}

// A key behind a command is still a key in the chat.
func TestACommandCarryingAKeyShowsNoPanel(t *testing.T) {
	b, _, client := newBot()
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start "+openAIKey)); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 || client.calls[0].method != "deleteMessage" || client.calls[1].text != i18n.T("en", "msg.key_deleted") {
		t.Fatalf("calls = %+v", client.calls)
	}
}

func TestTheAnswerIsInThePersonsLanguage(t *testing.T) {
	for _, lang := range i18n.Codes() {
		b, store, client := newBot()
		store.userLang[7] = lang
		if err := b.onMessage(context.Background(), message(dmChat, 7, openAIKey)); err != nil {
			t.Fatal(err)
		}
		want := i18n.T(lang, "msg.key_deleted")
		if got := client.last().text; got != want || strings.HasPrefix(got, "[") {
			t.Errorf("%s: answered %q, want %q", lang, got, want)
		}
	}
}

// failingDelete is a Telegram that refuses to delete.
type failingDelete struct{ *fakeTelegram }

func (failingDelete) DeleteMessage(context.Context, int64, int64) error {
	return errors.New("Bad Request: message can't be deleted")
}

// A message that stayed must not be called deleted.
func TestAKeyThatCannotBeDeletedSaysSo(t *testing.T) {
	var logs bytes.Buffer
	client := newTelegram()
	b := New(newStore(), failingDelete{client}, slog.New(slog.NewTextHandler(&logs, nil)), "framytest_bot", "v0.1.0")
	if err := b.onMessage(context.Background(), message(dmChat, 7, openRouterKey)); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || client.calls[0].method != "sendMessage" || client.calls[0].text != i18n.T("en", "msg.key_kept") {
		t.Fatalf("calls = %+v", client.calls)
	}
	if i18n.T("en", "msg.key_kept") == i18n.T("en", "msg.key_deleted") {
		t.Error("the two answers are the same")
	}
	if !strings.Contains(logs.String(), "could not be deleted") {
		t.Error("the failure was not logged")
	}
	if strings.Contains(logs.String(), openRouterKey) || strings.Contains(logs.String(), openRouterKey[:20]) {
		t.Error("the log holds the key")
	}
}

func TestOnlyAKeyInADMIsDeleted(t *testing.T) {
	for name, tc := range map[string]struct {
		chat tg.Chat
		from *tg.User
		text string
	}{
		"a key in a group":        {groupChat, &tg.User{ID: 7}, openAIKey},
		"a bot's message":         {dmChat, &tg.User{ID: 7, IsBot: true}, openAIKey},
		"no sender":               {dmChat, nil, openAIKey},
		"an ordinary message":     {dmChat, &tg.User{ID: 7}, "a red fox in the snow"},
		"a word ending in sk-":    {dmChat, &tg.User{ID: 7}, "task-" + strings.Repeat("a1", 12)},
		"a short sk-":             {dmChat, &tg.User{ID: 7}, "sk-1234"},
		"prose with an sk- dash":  {dmChat, &tg.User{ID: 7}, "use sk-learn-pipeline-example for it"},
		"a short AIza word":       {dmChat, &tg.User{ID: 7}, "AIza123"},
		"a link with a long path": {dmChat, &tg.User{ID: 7}, "https://example.com/a/task-" + strings.Repeat("Ab1", 10)},
	} {
		t.Run(name, func(t *testing.T) {
			b, _, client := newBot()
			m := message(tc.chat, 7, tc.text)
			m.From = tc.from
			if err := b.onMessage(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			if len(client.calls) != 0 {
				t.Fatalf("calls = %+v", client.calls)
			}
		})
	}
}

// The path an update takes: touched like any other, then guarded.
func TestAKeyInAnUpdateIsDeletedAfterTheTouch(t *testing.T) {
	b, store, client := newBot()
	b.handle(context.Background(), tg.Update{UpdateID: 1, Message: message(dmChat, 7, geminiKey)})
	if len(store.touches) != 1 || store.touches[0].userID != 7 {
		t.Errorf("touches = %+v", store.touches)
	}
	if len(client.calls) != 2 || client.calls[0].method != "deleteMessage" {
		t.Errorf("calls = %+v", client.calls)
	}
}
