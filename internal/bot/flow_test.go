// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"slices"
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
	replies   []string
	callbacks []string
	inline    []string
	guests    []string
	mentions  []string
	open      int
}

type flowCommand struct{ command, prompt string }

func (f *fakeFlow) Command(_ context.Context, _ *tg.Message, command, prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, flowCommand{command, prompt})
}

func (f *fakeFlow) Mention(_ context.Context, _ *tg.Message, prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mentions = append(f.mentions, prompt)
}

func (f *fakeFlow) Plain(_ context.Context, m *tg.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plain = append(f.plain, m.Text+m.Caption)
}

func (f *fakeFlow) Reply(_ context.Context, m *tg.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, m.Text)
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

func (f *fakeFlow) Guest(_ context.Context, _ *tg.Message, prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guests = append(f.guests, prompt)
}

func (f *fakeFlow) GuestOpen(context.Context, *tg.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open++
}

// framy streams no drafts: a Stop on one, which framy does not ask
// Telegram for, touches nobody and reaches nothing.
func TestAStopOnADraftIsLeftAlone(t *testing.T) {
	flow := &fakeFlow{}
	b, store, client := newBotWith(WithFlow(flow))
	b.handle(context.Background(), tg.Update{UpdateID: 1, StoppedMessageGeneration: &tg.MessageGenerationStopped{Chat: dmChat, DraftID: 41}})
	if len(store.touches) != 0 || len(client.calls) != 0 || len(flow.callbacks) != 0 {
		t.Fatalf("touches %+v, calls %+v, callbacks %v", store.touches, client.calls, flow.callbacks)
	}
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

// A press on a button of an inline message has no message. One of a run,
// which a guest run's answer is, goes to the flow; any other is answered.
// Either way only its sender is touched, and no panel is reached.
func TestAPressOnAnInlineMessageGoesToTheFlowOnlyIfARunsIs(t *testing.T) {
	flow := &fakeFlow{}
	b, store, client := newBotWith(WithFlow(flow))
	b.handle(context.Background(), tg.Update{UpdateID: 1, Callback: &tg.CallbackQuery{
		ID: "cq", From: tg.User{ID: 7}, InlineMessageID: "im", Data: "j:5:g",
	}})
	if len(flow.callbacks) != 1 || flow.callbacks[0] != "j:5:g" || len(client.answers) != 0 {
		t.Fatalf("flow got %v, the bot answered %+v", flow.callbacks, client.answers)
	}
	b.handle(context.Background(), tg.Update{UpdateID: 2, Callback: &tg.CallbackQuery{
		ID: "cq2", From: tg.User{ID: 7}, InlineMessageID: "im", Data: "h:7:new",
	}})
	if len(flow.callbacks) != 1 || len(client.answers) != 1 || client.answers[0] != "cq2" || len(client.calls) != 0 {
		t.Fatalf("flow got %v, the bot answered %+v, sent %+v", flow.callbacks, client.answers, client.calls)
	}
	if want := (touch{7, 0}); len(store.touches) != 2 || store.touches[0] != want || store.touches[1] != want {
		t.Fatalf("touches = %+v", store.touches)
	}

	// Without a flow every one is answered.
	b, _, client = newBot()
	b.handle(context.Background(), tg.Update{UpdateID: 3, Callback: &tg.CallbackQuery{
		ID: "cq", From: tg.User{ID: 7}, InlineMessageID: "im", Data: "j:5:g",
	}})
	if len(client.answers) != 1 {
		t.Fatalf("answers = %+v", client.answers)
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

// "@framytest_bot a fox" at the start of a group message is /img by name,
// as it is in a chat framy is not in. A mention later on is talk about
// framy, another bot's name is not framy's, and a result posted through
// framy's inline mode asks for nothing.
func TestAMentionInAGroupGoesToTheFlow(t *testing.T) {
	flow := &fakeFlow{}
	b, _, client := newBotWith(WithFlow(flow))
	ctx := context.Background()
	for _, tc := range []struct {
		text, caption string
		raw           string
		want          []string
	}{
		{text: "@framytest_bot a fox in the snow", want: []string{"a fox in the snow"}},
		{text: "  @FramyTest_Bot, a fox", want: []string{"a fox"}},
		{text: "@framytest_bot: make it\nnight", want: []string{"make it\nnight"}},
		{caption: "@framytest_bot make it snow", want: []string{"make it snow"}},
		// Only the mention: the flow says what to write.
		{text: "@framytest_bot", want: []string{""}},
		{text: "hey @framytest_bot a fox"},
		{text: "@framytest_bot2 a fox"},
		{text: "@framytest_botanist a fox"},
		{text: "@other_bot a fox"},
		{text: "a fox"},
		{text: "@framytest_bot a fox", raw: `{"via_bot":{"id":1}}`},
	} {
		flow.mentions, flow.commands = nil, nil
		m := message(groupChat, 7, tc.text)
		m.Caption = tc.caption
		if tc.raw != "" {
			m.Raw = []byte(tc.raw)
		}
		if err := b.onMessage(ctx, m); err != nil {
			t.Fatalf("%q: %v", tc.text+tc.caption, err)
		}
		if !slices.Equal(flow.mentions, tc.want) || len(flow.commands) != 0 {
			t.Errorf("%q gave %q (commands %+v), want %q", tc.text+tc.caption, flow.mentions, flow.commands, tc.want)
		}
	}
	if len(client.calls) != 0 {
		t.Errorf("the bot answered itself: %+v", client.calls)
	}
	// In the DM a message needs no mention: it is a plain one.
	flow.mentions = nil
	if err := b.onMessage(ctx, message(dmChat, 7, "@framytest_bot a fox")); err != nil {
		t.Fatal(err)
	}
	if len(flow.mentions) != 0 || len(flow.plain) != 1 {
		t.Errorf("DM: mentions %q, plain %q", flow.mentions, flow.plain)
	}
	// A bot's message is never a request.
	m := message(groupChat, 7, "@framytest_bot a fox")
	m.From.IsBot = true
	if err := b.onMessage(ctx, m); err != nil || len(flow.mentions) != 0 {
		t.Errorf("a bot: %v, %q", err, flow.mentions)
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

// In a group, words that answer one of framy's own messages go to the flow,
// which takes them when they answer a result of the sender's; chatter, a
// reply to someone else, another bot's command and a bot's words do not.
func TestAGroupsReplyToFramyGoesToTheFlow(t *testing.T) {
	flow := &fakeFlow{}
	b, _, _ := newBotWith(WithFlow(flow))
	toFramy := func(m *tg.Message) *tg.Message {
		m.ReplyToMessage = &tg.Message{MessageID: 5, Chat: groupChat, From: &tg.User{ID: selfID, IsBot: true}}
		return m
	}
	for _, tc := range []struct {
		name    string
		m       *tg.Message
		reaches bool
	}{
		{"words to framy", toFramy(message(groupChat, 7, "make it brighter")), true},
		{"chatter", message(groupChat, 7, "make it brighter"), false},
		{"words to a person", func() *tg.Message {
			m := message(groupChat, 7, "make it brighter")
			m.ReplyToMessage = &tg.Message{MessageID: 5, Chat: groupChat, From: &tg.User{ID: 8}}
			return m
		}(), false},
		{"another bot's command", toFramy(message(groupChat, 7, "/foo@other_bot bar")), false},
		{"a bot", toFramy(&tg.Message{MessageID: 13, Chat: groupChat, From: &tg.User{ID: 8, IsBot: true}, Text: "a fox"}), false},
		{"a command", toFramy(message(groupChat, 7, "/img make it brighter")), false},
	} {
		flow.replies = nil
		if err := b.onMessage(context.Background(), tc.m); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := len(flow.replies) == 1; got != tc.reaches {
			t.Errorf("%s: reached the flow %v, want %v", tc.name, flow.replies, tc.reaches)
		}
		if len(flow.plain) != 0 {
			t.Fatalf("%s: taken as a direct message", tc.name)
		}
	}
	// Asked by name, a reply to framy is a request as /img is: the mention
	// wins.
	flow.replies, flow.mentions = nil, nil
	if err := b.onMessage(context.Background(), toFramy(message(groupChat, 7, "@framytest_bot make it night"))); err != nil {
		t.Fatal(err)
	}
	if len(flow.replies) != 0 || !slices.Equal(flow.mentions, []string{"make it night"}) {
		t.Errorf("a named reply: replies %q, mentions %q", flow.replies, flow.mentions)
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
