// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// tooMany is Telegram asking to wait d before writing to the chat again.
func tooMany(d time.Duration) *tg.APIError {
	return &tg.APIError{StatusCode: 429, ErrorCode: 429, Description: "Too Many Requests: retry after", RetryAfter: d}
}

// stopClock gives the pace a clock of its own that moves only when the
// pace sleeps or the test moves it, and returns the sleeps it made.
func stopClock(h *harness) (move func(time.Duration), slept *[]time.Duration) {
	at := clock
	var sleeps []time.Duration
	h.flow.pace.now = func() time.Time { return at }
	h.flow.pace.sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		at = at.Add(d)
		return nil
	}
	return func(d time.Duration) { at = at.Add(d) }, &sleeps
}

// A 429 holds that chat until its wait is over: an answer to a person waits
// for it and goes, a round of the follow loop does not wait and sends
// nothing, a wait past maxHold is not waited for, and other chats are not
// held at all.
func TestA429HoldsTheChat(t *testing.T) {
	h := newHarness(t)
	move, slept := stopClock(h)
	ctx := context.Background()
	send := func(ctx context.Context, chat int64) error {
		_, err := h.flow.cfg.Telegram.SendReply(ctx, chat, 10, 0, "hi", nil)
		return err
	}
	h.tg.fail["sendMessage"] = tooMany(7 * time.Second)
	if err := send(ctx, group.ID); !tg.IsTooManyRequests(err) {
		t.Fatalf("the 429 itself: %v", err)
	}
	delete(h.tg.fail, "sendMessage")
	h.tg.reset()
	if err := send(noWait(ctx), group.ID); !errors.Is(err, errHeld) || len(h.tg.calls) != 0 {
		t.Fatalf("a round: %v, calls %+v", err, h.tg.calls)
	}
	if err := send(ctx, -200); err != nil || len(h.tg.calls) != 1 || len(*slept) != 0 {
		t.Fatalf("another chat: %v, calls %+v, slept %v", err, h.tg.calls, *slept)
	}
	if err := send(ctx, group.ID); err != nil || len(h.tg.calls) != 2 || len(*slept) != 1 || (*slept)[0] != 7*time.Second {
		t.Fatalf("an answer: %v, calls %+v, slept %v", err, h.tg.calls, *slept)
	}
	if h.flow.pace.held(group.ID) {
		t.Fatal("still held after the wait")
	}

	// A flood wait of minutes is not waited out by a command.
	h.tg.fail["sendMessage"] = tooMany(5 * time.Minute)
	_ = send(ctx, group.ID)
	delete(h.tg.fail, "sendMessage")
	if err := send(ctx, group.ID); !errors.Is(err, errHeld) || len(*slept) != 1 {
		t.Fatalf("a long wait: %v, slept %v", err, *slept)
	}
	move(5 * time.Minute)
	if err := send(noWait(ctx), group.ID); err != nil {
		t.Fatalf("after the long wait: %v", err)
	}
}

// tg waits out the first 429 inside the call; a call that only shows
// progress is cut short so the round does not stall, and the wait it was
// told still holds the chat.
func TestACutShortWaitStillHolds(t *testing.T) {
	h := newHarness(t)
	_, _ = stopClock(h)
	err := h.flow.cfg.Telegram.SendChatAction(context.Background(), group.ID, 0, "upload_photo")
	if err != nil {
		t.Fatal(err)
	}
	h.tg.fail["sendChatAction"] = errors.Join(tooMany(20*time.Second), context.DeadlineExceeded)
	_ = h.flow.cfg.Telegram.SendChatAction(context.Background(), group.ID, 0, "upload_photo")
	if !h.flow.pace.held(group.ID) {
		t.Fatal("not held")
	}
}

// generatingIn makes a run of a command in chat, presses Generate and has
// the job running.
func (h *harness) generatingIn(t *testing.T, chat tg.Chat, message int64) int64 {
	t.Helper()
	m := command("/img a fox")
	m.Chat, m.MessageID = chat, message
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	id := int64(len(h.store.jobs))
	if err := h.pressOn(id, 7, actGenerate, tg.Message{MessageID: h.runOf(t, id).PreviewMessageID, Chat: chat, Date: 1}); err != nil {
		t.Fatal(err)
	}
	h.store.setStatus(id, jobs.Running, "")
	return id
}

func (h *harness) runOf(t *testing.T, id int64) db.ChatRun {
	t.Helper()
	run, err := h.store.ChatRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// A held chat gets nothing from the follow loop, so its result waits for
// the hold to end, while another chat's result goes at once.
func TestFollowSkipsAHeldChat(t *testing.T) {
	h := newHarness(t)
	move, _ := stopClock(h)
	other := tg.Chat{ID: -200, Type: "supergroup"}
	held, free := h.generatingIn(t, group, 10), h.generatingIn(t, other, 20)
	for _, id := range []int64{held, free} {
		h.store.setStatus(id, jobs.Done, "")
		h.files.results[id] = []storage.Asset{{ID: 50 + id, ContentType: "image/png", Bytes: 100, Width: 10, Height: 10}}
	}
	h.flow.pace.hold(group.ID, 30*time.Second)
	h.tg.reset()
	h.flow.follow(context.Background())
	if h.store.stage(held) != db.StageGenerating || h.store.stage(free) != db.StageDone {
		t.Fatalf("stages %s and %s", h.store.stage(held), h.store.stage(free))
	}
	for _, c := range h.tg.calls {
		if c.chatID == group.ID {
			t.Fatalf("a call to the held chat: %+v", c)
		}
	}
	move(31 * time.Second)
	h.flow.follow(context.Background())
	if h.store.stage(held) != db.StageDone {
		t.Fatalf("after the hold: %s", h.store.stage(held))
	}
}

// The loop of a job deleted on the site while Telegram holds its chat goes
// after the hold, not in a round that would wait on it.
func TestATakeDownWaitsOutAHeldChat(t *testing.T) {
	h := newHarness(t)
	move, _ := stopClock(h)
	id := h.generatingIn(t, group, 10)
	loop := run(t, h).PreviewMessageID
	h.store.deleteJob(id)
	h.flow.pace.hold(group.ID, 30*time.Second)
	h.tg.reset()
	h.flow.follow(context.Background())
	if len(h.tg.calls) != 0 {
		t.Fatalf("calls in the hold: %+v", h.tg.calls)
	}
	if _, ok := h.store.runs[id]; !ok {
		t.Fatal("the run went before its loop")
	}
	move(31 * time.Second)
	h.flow.follow(context.Background())
	if del := h.tg.methods("deleteMessage"); len(del) != 1 || del[0].messageID != loop {
		t.Fatalf("after the hold: %+v", h.tg.calls)
	}
	if _, ok := h.store.runs[id]; ok {
		t.Error("the run stayed")
	}
}

// Three runs in one group take turns on the captions, one every 6 s at
// most and each every 18 s, and share one chat action: about 10 edits a
// minute where they made 18 and 40 actions.
func TestRunsOfAChatShareItsPace(t *testing.T) {
	h := newHarness(t)
	ids := []int64{h.generatingIn(t, group, 10), h.generatingIn(t, group, 11), h.generatingIn(t, group, 12)}
	h.tg.reset()
	ctx := context.Background()
	edits := map[int64][]time.Duration{}
	start := h.now
	for at := 2 * time.Second; at <= 60*time.Second; at += 2 * time.Second {
		h.now = start.Add(at)
		before := len(h.tg.methods("editMessageCaption"))
		h.flow.follow(ctx)
		caps := h.tg.methods("editMessageCaption")
		if len(caps)-before > 1 {
			t.Fatalf("at %s: %d captions in one round", at, len(caps)-before)
		}
		for _, c := range caps[before:] {
			for _, id := range ids {
				if c.messageID == h.runOf(t, id).PreviewMessageID {
					edits[id] = append(edits[id], at)
				}
			}
		}
	}
	total := 0
	for _, id := range ids {
		got := edits[id]
		total += len(got)
		if len(got) < 2 {
			t.Errorf("run %d told its time %d times in a minute: %v", id, len(got), got)
		}
		for i := 1; i < len(got); i++ {
			if got[i]-got[i-1] < 18*time.Second {
				t.Errorf("run %d: captions %v apart", id, got[i]-got[i-1])
			}
		}
	}
	if total > 10 {
		t.Errorf("%d captions in a minute", total)
	}
	if n := len(h.tg.methods("sendChatAction")); n > 14 {
		t.Errorf("%d chat actions in a minute, as if each run sent its own", n)
	}
}

// A result of the site that Telegram asked to wait with is given back and
// goes after the wait: a 429 took nothing. It used to be lost for good.
func TestASiteResultTelegramDeferredGoesLater(t *testing.T) {
	h := newHarness(t)
	move, _ := stopClock(h)
	siteResult(t, h, "image", picture)
	h.tg.fail["sendPhoto"] = tooMany(10 * time.Second)
	h.flow.sendSite(context.Background())
	if d := h.store.delivery(siteJob); d != "" {
		t.Fatalf("delivery %q after a 429", d)
	}
	delete(h.tg.fail, "sendPhoto")
	h.tg.reset()
	// While the DM is held the round does not even claim it.
	h.flow.sendSite(context.Background())
	if len(h.tg.calls) != 0 || h.store.delivery(siteJob) != "" {
		t.Fatalf("held: delivery %q, calls %+v", h.store.delivery(siteJob), h.tg.calls)
	}
	move(11 * time.Second)
	h.flow.sendSite(context.Background())
	if h.store.delivery(siteJob) != db.DeliverySent || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("after the wait: delivery %q, calls %+v", h.store.delivery(siteJob), h.tg.calls)
	}
}

// A delete that cleans up after framy's own message, the loop under a
// result say, does not hold up its caller for a held chat, which in a round
// would stall every other chat, and is not dropped when the hold is longer
// than a command waits: it goes in the background once the wait is over.
// One that Telegram answers with a 429 goes the same way.
func TestACleanupWaitsOutTheHoldInTheBackground(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold time.Duration
		// tooMany: the chat is not held yet, and the delete itself gets the 429.
		tooMany bool
	}{
		{"held", 20 * time.Second, false},
		{"held past a command's wait", 5 * time.Minute, false},
		{"answered with a 429", 10 * time.Second, true},
	} {
		h := newHarness(t)
		var mu sync.Mutex
		at := clock
		var slept []time.Duration
		release := make(chan struct{})
		h.flow.pace.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
		h.flow.pace.sleep = func(_ context.Context, d time.Duration) error {
			<-release
			h.tg.mu.Lock()
			delete(h.tg.fail, "deleteMessage")
			h.tg.mu.Unlock()
			mu.Lock()
			slept, at = append(slept, d), at.Add(d)
			mu.Unlock()
			return nil
		}
		if tc.tooMany {
			h.tg.fail["deleteMessage"] = tooMany(tc.hold)
		} else {
			h.flow.pace.hold(group.ID, tc.hold)
		}
		done := make(chan struct{})
		go func() {
			h.flow.deleteMessage(noWait(context.Background()), group.ID, 77)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the delete held up its caller", tc.name)
		}
		close(release)
		h.flow.Wait()
		del := h.tg.methods("deleteMessage")
		if len(del) != 1 || del[0].chatID != group.ID || del[0].messageID != 77 || len(slept) != 1 || slept[0] != tc.hold {
			t.Errorf("%s: deletes %+v after sleeping %v", tc.name, del, slept)
		}
	}
}
