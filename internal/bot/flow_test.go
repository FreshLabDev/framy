// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/FreshLabDev/tg"
)

// fakeFlow records what the bot passes to the flow of /img.
type fakeFlow struct {
	mu        sync.Mutex
	commands  []flowCommand
	plain     []string
	callbacks []string
	inline    []string
	stops     []int64
}

type flowCommand struct{ command, prompt string }

func (f *fakeFlow) Command(_ context.Context, _ *tg.Message, command, prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, flowCommand{command, prompt})
}

func (f *fakeFlow) Plain(_ context.Context, m *tg.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plain = append(f.plain, m.Text+m.Caption)
}

func (f *fakeFlow) Owns(data string) bool {
	return strings.HasPrefix(data, "j:") || strings.HasPrefix(data, "d:")
}

func (f *fakeFlow) Callback(_ context.Context, cq *tg.CallbackQuery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, cq.Data)
}

func (f *fakeFlow) Inline(_ context.Context, q *tg.InlineQuery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inline = append(f.inline, q.Query)
}

func (f *fakeFlow) StopDraft(_ context.Context, s *tg.MessageGenerationStopped) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, s.DraftID)
}

// Stop on a draft of a DM goes to the flow; nobody is touched, since the
// update names no one.
func TestAStopOnADraftGoesToTheFlow(t *testing.T) {
	flow := &fakeFlow{}
	b, store, client := newBotWith(WithFlow(flow))
	ctx := context.Background()
	b.handle(ctx, tg.Update{UpdateID: 1, StoppedMessageGeneration: &tg.MessageGenerationStopped{Chat: dmChat, DraftID: 41}})
	// Telegram drafts only into private chats.
	b.handle(ctx, tg.Update{UpdateID: 2, StoppedMessageGeneration: &tg.MessageGenerationStopped{Chat: groupChat, DraftID: 42}})
	if len(flow.stops) != 1 || flow.stops[0] != 41 || len(store.touches) != 0 || len(client.calls) != 0 {
		t.Fatalf("stops %v, touches %+v, calls %+v", flow.stops, store.touches, client.calls)
	}
	b, _, _ = newBot()
	b.handle(ctx, tg.Update{UpdateID: 3, StoppedMessageGeneration: &tg.MessageGenerationStopped{Chat: dmChat, DraftID: 41}})
}

// An inline query goes to the flow, and its sender is touched with no chat,
// since the query does not name one.
func TestAnInlineQueryGoesToTheFlow(t *testing.T) {
	flow := &fakeFlow{}
	b, store, client := newBotWith(WithFlow(flow))
	b.handle(context.Background(), tg.Update{UpdateID: 1, InlineQuery: &tg.InlineQuery{ID: "q", From: tg.User{ID: 7}, Query: "fox"}})
	if len(flow.inline) != 1 || flow.inline[0] != "fox" || len(client.calls) != 0 {
		t.Fatalf("flow got %v, the bot sent %+v", flow.inline, client.calls)
	}
	if want := (touch{7, 0}); len(store.touches) != 1 || store.touches[0] != want {
		t.Fatalf("touches = %+v", store.touches)
	}

	// Without a flow there is nothing to answer with.
	b, _, client = newBot()
	b.handle(context.Background(), tg.Update{UpdateID: 2, InlineQuery: &tg.InlineQuery{ID: "q", From: tg.User{ID: 7}}})
	if len(client.calls) != 0 || len(client.answers) != 0 {
		t.Fatalf("the bot answered %+v %+v", client.calls, client.answers)
	}
}

// A press on a button of an inline message has no message: it is answered,
// touches its sender only, and reaches neither a panel nor the flow.
func TestAPressOnAnInlineMessageIsOnlyAnswered(t *testing.T) {
	flow := &fakeFlow{}
	b, store, client := newBotWith(WithFlow(flow))
	b.handle(context.Background(), tg.Update{UpdateID: 1, Callback: &tg.CallbackQuery{
		ID: "cq", From: tg.User{ID: 7}, InlineMessageID: "im", Data: "j:5:g",
	}})
	if len(flow.callbacks) != 0 || len(client.answers) != 1 || client.answers[0] != "cq" {
		t.Fatalf("flow got %v, the bot answered %+v", flow.callbacks, client.answers)
	}
	if want := (touch{7, 0}); len(store.touches) != 1 || store.touches[0] != want {
		t.Fatalf("touches = %+v", store.touches)
	}
}

func TestImgAndEditGoToTheFlow(t *testing.T) {
	flow := &fakeFlow{}
	b, _, _ := newBotWith(WithFlow(flow))
	ctx := context.Background()
	for _, tc := range []struct {
		text, caption string
		want          *flowCommand
	}{
		{text: "/img a fox in the snow", want: &flowCommand{"img", "a fox in the snow"}},
		{text: "/img@framytest_bot a fox", want: &flowCommand{"img", "a fox"}},
		{text: "/IMG a fox", want: &flowCommand{"img", "a fox"}},
		{text: "/img\na fox\nin the snow", want: &flowCommand{"img", "a fox\nin the snow"}},
		{text: "/img", want: &flowCommand{"img", ""}},
		{text: "/edit make it rain", want: &flowCommand{"edit", "make it rain"}},
		// /img as the caption of one's own photo.
		{caption: "/img make it night", want: &flowCommand{"img", "make it night"}},
		{text: "/img@other_bot a fox"},
		{text: "a fox /img"},
	} {
		flow.commands = nil
		m := message(groupChat, 7, tc.text)
		m.Caption = tc.caption
		if err := b.onMessage(ctx, m); err != nil {
			t.Fatalf("%q: %v", tc.text+tc.caption, err)
		}
		switch {
		case tc.want == nil && len(flow.commands) != 0:
			t.Errorf("%q reached the flow: %+v", tc.text, flow.commands)
		case tc.want != nil && (len(flow.commands) != 1 || flow.commands[0] != *tc.want):
			t.Errorf("%q gave %+v, want %+v", tc.text+tc.caption, flow.commands, *tc.want)
		}
	}
}

func TestWithoutAFlowImgIsLeftUnanswered(t *testing.T) {
	b, _, client := newBot()
	if err := b.onMessage(context.Background(), message(groupChat, 7, "/img a fox")); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("sent %+v", client.calls)
	}
}

func TestARunsButtonGoesToTheFlowAndAMenuButtonDoesNot(t *testing.T) {
	flow := &fakeFlow{}
	b, _, client := newBotWith(WithFlow(flow))
	ctx := context.Background()
	run := callback(groupChat, 7, 7, "", 0)
	run.Data = "j:42:go"
	if err := b.onCallback(ctx, run); err != nil {
		t.Fatal(err)
	}
	if len(flow.callbacks) != 1 || flow.callbacks[0] != "j:42:go" {
		t.Fatalf("flow got %v", flow.callbacks)
	}
	// The flow answers its own presses.
	if len(client.answers) != 0 {
		t.Fatalf("the bot answered a run's press: %+v", client.answers)
	}
	if err := b.onCallback(ctx, callback(dmChat, 7, 7, actHelp, 0)); err != nil {
		t.Fatal(err)
	}
	if len(flow.callbacks) != 1 {
		t.Fatalf("a menu press reached the flow: %v", flow.callbacks)
	}
}

// A direct message that is no command goes to the flow, which asks what to
// make of it; a group's plain chatter, an unknown command and a key do not.
func TestAPlainDirectMessageGoesToTheFlow(t *testing.T) {
	flow := &fakeFlow{}
	b, _, client := newBotWith(WithFlow(flow))
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		m       *tg.Message
		reaches bool
	}{
		{"words", message(dmChat, 7, "a fox in the snow"), true},
		{"a photo with words", &tg.Message{MessageID: 11, Chat: dmChat, From: &tg.User{ID: 7}, Caption: "make it night", Photo: []tg.Photo{{FileID: "p"}}}, true},
		{"a photo alone", &tg.Message{MessageID: 12, Chat: dmChat, From: &tg.User{ID: 7}, Photo: []tg.Photo{{FileID: "p"}}}, true},
		{"a group", message(groupChat, 7, "a fox in the snow"), false},
		{"an unknown command", message(dmChat, 7, "/foo bar"), false},
		{"another bot's command", message(dmChat, 7, "/img@other_bot a fox"), false},
		{"a bot", &tg.Message{MessageID: 13, Chat: dmChat, From: &tg.User{ID: 8, IsBot: true}, Text: "a fox"}, false},
		{"a command", message(dmChat, 7, "/img a fox"), false},
		{"a key", message(dmChat, 7, openRouterKey), false},
	} {
		flow.plain = nil
		if err := b.onMessage(ctx, tc.m); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := len(flow.plain) == 1; got != tc.reaches {
			t.Errorf("%s: reached the flow %v, want %v", tc.name, flow.plain, tc.reaches)
		}
	}
	// The answers to the question are the flow's to work.
	answer := callback(dmChat, 7, 7, "", 0)
	answer.Data = "d:11:v"
	if err := b.onCallback(ctx, answer); err != nil {
		t.Fatal(err)
	}
	if len(flow.callbacks) != 1 || flow.callbacks[0] != "d:11:v" || len(client.answers) != 0 {
		t.Fatalf("flow got %v, the bot answered %+v", flow.callbacks, client.answers)
	}
}

// Without a flow a plain message stays unanswered, as before.
func TestWithoutAFlowAPlainMessageIsLeftUnanswered(t *testing.T) {
	b, _, client := newBot()
	if err := b.onMessage(context.Background(), message(dmChat, 7, "a fox")); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("sent %+v", client.calls)
	}
}
