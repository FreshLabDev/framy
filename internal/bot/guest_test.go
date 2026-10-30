// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"
)

// guestMessage is a guest message from person 7 in a chat framy is not in.
func guestMessage(text string) *tg.Message {
	return &tg.Message{
		From:         &tg.User{ID: 7, FirstName: "Ann"},
		Chat:         tg.Chat{ID: -300, Type: "supergroup"},
		Text:         text,
		GuestQueryID: "gq",
	}
}

// A guest message goes to the flow without framy's mention, and only its
// sender is touched: its chat id may name another chat framy knows.
func TestAGuestMessageGoesToTheFlowWithoutTheMention(t *testing.T) {
	flow := &fakeFlow{}
	b, store, client := newBotWith(WithFlow(flow))
	b.handle(context.Background(), tg.Update{UpdateID: 1, GuestMessage: guestMessage("@FramyTest_Bot  make it snow")})
	if len(flow.guests) != 1 || flow.guests[0] != "make it snow" || len(client.calls) != 0 {
		t.Fatalf("flow got %q, the bot sent %+v", flow.guests, client.calls)
	}
	if want := (touch{7, 0}); len(store.touches) != 1 || store.touches[0] != want {
		t.Fatalf("touches = %+v", store.touches)
	}
	if len(flow.commands) != 0 || len(flow.plain) != 0 {
		t.Fatalf("taken as a message of a chat: %v %v", flow.commands, flow.plain)
	}
}

// Another bot's guest message, and one with no query to answer, are left
// alone; without a flow there is nothing to answer with.
func TestAGuestMessageFramyCannotAnswerIsLeftAlone(t *testing.T) {
	flow := &fakeFlow{}
	b, _, client := newBotWith(WithFlow(flow))
	fromBot := guestMessage("@framytest_bot make it snow")
	fromBot.From.IsBot = true
	noQuery := guestMessage("@framytest_bot make it snow")
	noQuery.GuestQueryID = ""
	b.handle(context.Background(), tg.Update{UpdateID: 1, GuestMessage: fromBot})
	b.handle(context.Background(), tg.Update{UpdateID: 2, GuestMessage: noQuery})
	if len(flow.guests) != 0 || len(client.calls) != 0 {
		t.Fatalf("flow got %q, the bot sent %+v", flow.guests, client.calls)
	}

	b, _, client = newBot()
	b.handle(context.Background(), tg.Update{UpdateID: 3, GuestMessage: guestMessage("@framytest_bot make it snow")})
	if len(client.calls) != 0 {
		t.Fatalf("the bot sent %+v", client.calls)
	}
}

func TestGuestPrompt(t *testing.T) {
	for _, tc := range []struct{ text, caption, want string }{
		{text: "@framytest_bot make it snow", want: "make it snow"},
		{text: "make it @FRAMYTEST_BOT snow", want: "make it snow"},
		// A reply to framy's answer comes with no mention.
		{text: "now at night", want: "now at night"},
		{caption: "@framytest_bot a fox", want: "a fox"},
		{text: "@framytest_bot", want: ""},
		// Another bot's mention is the person's words.
		{text: "@framytest_bot @gif cats", want: "@gif cats"},
		// A longer name is someone else's.
		{text: "@framytest_bot_two cats", want: "@framytest_bot_two cats"},
		{text: "@framytest_bot\ta fox\nat night", want: "a fox\nat night"},
	} {
		m := &tg.Message{Text: tc.text, Caption: tc.caption}
		if got := guestPrompt(m, "framytest_bot"); got != tc.want {
			t.Errorf("guestPrompt(%q, %q) = %q, want %q", tc.text, tc.caption, got, tc.want)
		}
	}
}
