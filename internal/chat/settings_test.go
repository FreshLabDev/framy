// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"reflect"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// A refusal a key would lift has the way to that key under it: in the DM
// the Mini App's settings open at it; in a group, where Telegram takes no
// Mini App button, a link opens framy's DM at it. Without either address
// the refusal is the text alone, as before.
func TestARefusalLeadsToTheKey(t *testing.T) {
	for _, tc := range []struct {
		name     string
		chat     tg.Chat
		app, bot string
		want     [][]string
		label    string
	}{
		{"group", group, "https://framy.test", "framytest_bot", [][]string{{"url:https://t.me/framytest_bot?start=settings_openrouter"}}, i18n.T("en", "btn.open")},
		{"dm", dm, "https://framy.test", "framytest_bot", [][]string{{"app:https://framy.test/?settings=openrouter"}}, "Connect OpenRouter"},
		// A DM without a site still gets the link, to the panel of the DM.
		{"dm without a site", dm, "", "framytest_bot", [][]string{{"url:https://t.me/framytest_bot?start=settings_openrouter"}}, i18n.T("en", "btn.open")},
		{"group without a name", group, "https://framy.test", "", nil, ""},
	} {
		h := newHarness(t)
		h.flow.cfg.AppURL, h.flow.cfg.Username = tc.app, tc.bot
		h.queue.refuse = &free.ClosedError{}
		m := command("/img a fox")
		m.Chat = tc.chat
		if err := h.command(m, CommandImage, "a fox"); err != nil {
			t.Fatal(err)
		}
		got := h.tg.methods("sendMessage")
		if len(got) != 1 || got[0].text != closedText("en", catalog.Image) {
			t.Errorf("%s: answer %+v", tc.name, got)
			continue
		}
		if kb := buttons(got[0].markup); !reflect.DeepEqual(kb, tc.want) {
			t.Errorf("%s: buttons %v, want %v", tc.name, kb, tc.want)
			continue
		}
		if tc.want != nil {
			if b := got[0].markup.InlineKeyboard[0][0]; b.Text != tc.label || b.Style != tg.StylePrimary {
				t.Errorf("%s: button %+v", tc.name, b)
			}
		}
	}
}

// A guest's chat has framy as no member: the refusal links to framy's DM.
func TestAGuestRefusalLeadsToTheKey(t *testing.T) {
	h := guestHarness(t)
	h.flow.cfg.Username = "framytest_bot"
	h.queue.refuse = &free.ClosedError{}
	if err := h.flow.guest(context.Background(), guestReply("gq-1"), "make it snow"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.guestAnswers) != 1 {
		t.Fatalf("answers = %+v", h.tg.guestAnswers)
	}
	a, ok := h.tg.guestAnswers[0].(tg.InlineQueryResultArticle)
	if !ok || !reflect.DeepEqual(buttons(a.ReplyMarkup), [][]string{{"url:https://t.me/framytest_bot?start=settings_openrouter"}}) {
		t.Fatalf("answer = %+v", h.tg.guestAnswers[0])
	}
}

// An error card of a run on the person's own key leads to what fixes it: a
// refused key is replaced in the settings, an empty one is topped up at the
// provider. In a group and a guest's chat the settings are reached through
// framy's DM.
func TestAFailedKeyLeadsToTheFix(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.AppURL, h.flow.cfg.Username = "https://framy.test", "framytest_bot"
	link := "url:https://t.me/framytest_bot?start=settings_openai"
	for _, tc := range []struct {
		name  string
		run   db.ChatRun
		want  []string
		label string
	}{
		{"dm", db.ChatRun{ChatID: 7}, []string{"app:https://framy.test/?settings=openai"}, "Replace the OpenAI key"},
		{"group", db.ChatRun{ChatID: -100}, []string{link}, i18n.T("en", "btn.open")},
		{"guest", db.ChatRun{ChatID: 7, InlineMessageID: "inline-gq-1"}, []string{link}, i18n.T("en", "btn.open")},
		{"money", db.ChatRun{ChatID: 7, ErrorClass: string(jobs.Money)}, []string{"url:https://platform.openai.com/settings/organization/billing/overview"}, "Top up OpenAI"},
		{"free", db.ChatRun{ChatID: 7, Free: true}, nil, ""},
		{"other", db.ChatRun{ChatID: 7, ErrorClass: string(jobs.Internal)}, nil, ""},
	} {
		run := tc.run
		run.Lang, run.OfferID = "en", "openai:gpt-image-1"
		if run.ErrorClass == "" {
			run.ErrorClass = string(jobs.Key)
		}
		got := h.flow.keyFix(run)
		var data []string
		for _, b := range got {
			data = append(data, buttons(&tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{b}}})[0][0])
		}
		if !reflect.DeepEqual(data, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, data, tc.want)
			continue
		}
		if len(got) > 0 && got[0].Text != tc.label {
			t.Errorf("%s: labelled %q, want %q", tc.name, got[0].Text, tc.label)
		}
	}
}
