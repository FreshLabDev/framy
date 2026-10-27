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
	callbacks []string
}

type flowCommand struct{ command, prompt string }

func (f *fakeFlow) Command(_ context.Context, _ *tg.Message, command, prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, flowCommand{command, prompt})
}

func (f *fakeFlow) Owns(data string) bool { return strings.HasPrefix(data, "j:") }

func (f *fakeFlow) Callback(_ context.Context, cq *tg.CallbackQuery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, cq.Data)
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
