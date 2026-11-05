// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

var openRouterKey = "sk-or-v1-" + strings.Repeat("0123456789abcdef", 4)

// A key after /img in a group is no prompt: it goes to no model, no job and
// no caption. The command is deleted where framy may, and the answer says
// where keys go, with the way to the settings at that key's provider.
func TestAKeyInACommandRunsNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       *tg.Message
		prompt  string
		refuse  bool // Telegram refuses the delete
		deleted bool
	}{
		{"in the prompt", command("/img a fox " + openRouterKey), "a fox " + openRouterKey, false, true},
		{"in a caption", func() *tg.Message {
			m := photoReply("")
			m.Text, m.Caption = "", "/img "+openRouterKey
			return m
		}(), openRouterKey, false, true},
		// framy is no admin there: the person is asked to delete it.
		{"not deleted", command("/img " + openRouterKey), openRouterKey, true, false},
	} {
		h := newHarness(t)
		h.flow.cfg.Username = "framytest_bot"
		if tc.refuse {
			h.tg.fail["deleteMessage"] = errBoom
		}
		if err := h.command(tc.m, CommandImage, tc.prompt); err != nil {
			t.Fatal(err)
		}
		if len(h.store.jobs) != 0 || len(h.tg.methods("sendChatAction")) != 0 || len(h.tg.methods("sendRichMessage")) != 0 {
			t.Errorf("%s: a run started: %d jobs, %+v", tc.name, len(h.store.jobs), h.tg.calls)
			continue
		}
		if del := h.tg.methods("deleteMessage"); tc.deleted != (len(del) == 1 && del[0].chatID == group.ID && del[0].messageID == 10) {
			t.Errorf("%s: deletes %+v", tc.name, del)
		}
		want := "msg.key_deleted"
		if !tc.deleted {
			want = "msg.key_kept"
		}
		got := h.tg.methods("sendMessage")
		if len(got) != 1 || got[0].text != i18n.T("en", want) || strings.Contains(got[0].text, "sk-or") {
			t.Errorf("%s: answer %+v", tc.name, got)
			continue
		}
		if kb := buttons(got[0].markup); !reflect.DeepEqual(kb, [][]string{{"url:https://t.me/framytest_bot?start=settings_openrouter"}}) {
			t.Errorf("%s: buttons %v", tc.name, kb)
		}
	}
}

// A guest message with a key cannot be deleted by framy, which is not in
// the chat: the one answer asks the person to delete it. It needs no site.
func TestAKeyInAGuestMessageRunsNothing(t *testing.T) {
	h := guestHarness(t)
	h.flow.cfg.AppURL, h.flow.cfg.Username = "", "framytest_bot"
	m := guestReply("gq-1")
	m.Text = "@framytest_bot " + openRouterKey
	if err := h.flow.guest(context.Background(), m, openRouterKey); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 0 || len(h.tg.methods("deleteMessage")) != 0 || len(h.tg.guestAnswers) != 1 {
		t.Fatalf("jobs %d, calls %+v", len(h.store.jobs), h.tg.calls)
	}
	a, ok := h.tg.guestAnswers[0].(tg.InlineQueryResultArticle)
	if !ok || a.InputMessageContent.(tg.InputTextMessageContent).MessageText != i18n.T("en", "msg.key_kept") ||
		!reflect.DeepEqual(buttons(a.ReplyMarkup), [][]string{{"url:https://t.me/framytest_bot?start=settings_openrouter"}}) {
		t.Fatalf("answer = %+v", h.tg.guestAnswers[0])
	}
}

// Where framy is an admin, what went wrong with a command goes to its
// sender alone, as the preview does, and the chat reads none of it: a
// command with nothing to make, and the answer to a key, even once the
// command is gone. When Telegram refuses that, the chat sees it.
func TestARefusalInAGroupWhereFramyIsAdminIsTheSendersAlone(t *testing.T) {
	h := newHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	if err := h.command(command("/img"), CommandImage, ""); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].ephemeral == nil || sent[0].ephemeral.ReceiverUserID != 7 || sent[0].messageID != 10 ||
		sent[0].text != "<p>"+strings.ReplaceAll(i18n.T("en", "run.empty"), "\n", "<br>")+"</p>" || len(h.tg.methods("sendMessage")) != 0 {
		t.Fatalf("calls %+v", h.tg.calls)
	}

	h = newHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	if err := h.command(command("/img "+openRouterKey), CommandImage, openRouterKey); err != nil {
		t.Fatal(err)
	}
	sent = h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].ephemeral == nil || sent[0].messageID != 0 || !strings.Contains(sent[0].text, i18n.T("en", "msg.key_deleted")) {
		t.Fatalf("calls %+v", h.tg.calls)
	}

	h = newHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	h.tg.fail["sendRichMessage"] = &tg.APIError{Method: "sendRichMessage", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: not enough rights"}
	if err := h.command(command("/img"), CommandImage, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.empty") || got[0].messageID != 10 {
		t.Fatalf("the chat was told %+v", got)
	}
}
