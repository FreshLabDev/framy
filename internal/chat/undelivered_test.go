// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

var kicked = &tg.APIError{Method: "sendPhoto", StatusCode: 403, ErrorCode: 403, Description: "Forbidden: bot was kicked from the supergroup chat"}

// doneSlow is a run whose image is ready 40 s after Generate: it goes as a
// reply, a new message.
func (h *harness) doneSlow(t *testing.T) int64 {
	t.Helper()
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(40 * time.Second)
	return id
}

// A result Telegram refuses (the bot was removed, or may not post there) is
// refused the same way every time: the run ends at once, and nothing more
// is read or sent for it. Before, it was sent again every 2 s for good.
func TestAResultTelegramRefusesEndsTheRun(t *testing.T) {
	for _, refusal := range []*tg.APIError{
		kicked,
		{Method: "sendPhoto", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: message thread not found"},
	} {
		h := newHarness(t)
		id := h.doneSlow(t)
		h.tg.fail["sendPhoto"] = refusal
		h.flow.step(context.Background(), id)
		if h.store.stage(id) != db.StageGone {
			t.Fatalf("%s: stage %s", refusal.Description, h.store.stage(id))
		}
		// The loop says where the result is, and Stop goes from under it.
		caps := h.tg.methods("editMessageCaption")
		if len(caps) != 1 || caps[0].messageID != h.store.runs[id].PreviewMessageID || caps[0].markup != nil ||
			!strings.Contains(caps[0].text, i18n.T("en", "guest.elsewhere")) || !strings.Contains(caps[0].text, "a fox") {
			t.Fatalf("%s: the loop was told %+v", refusal.Description, caps)
		}
		opened := len(h.files.opened)
		for range 5 {
			h.now = h.now.Add(10 * time.Minute)
			h.flow.follow(context.Background())
		}
		if len(h.files.opened) != opened {
			t.Fatalf("%s: the result was read %d times more after the run ended", refusal.Description, len(h.files.opened)-opened)
		}
	}
}

// The same for a failed run whose card and reply are both refused: it ends
// failed, and the site's history says why.
func TestAFailureTelegramRefusesEndsTheRun(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Failed, jobs.Unavailable)
	h.tg.fail["editMessageMedia"] = kicked
	h.tg.fail["sendMessage"] = kicked
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageFailed {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

// Anything else may pass: it is tried again, less often each time, and not
// on every round.
func TestAResultThatFailsIsTriedAgainLessOften(t *testing.T) {
	h := newHarness(t)
	id := h.doneSlow(t)
	h.tg.fail["sendPhoto"] = &tg.APIError{Method: "sendPhoto", StatusCode: 502, ErrorCode: 502, Description: "Bad Gateway"}
	tries := func() int { return len(h.files.opened) }
	h.flow.step(context.Background(), id)
	if tries() != 1 || h.store.stage(id) != db.StageGenerating {
		t.Fatalf("tries %d, stage %s", tries(), h.store.stage(id))
	}
	// A round 2 s later waits: the next try is 2 s after the first, the one
	// after it 4 s after that.
	h.now = h.now.Add(time.Second)
	h.flow.step(context.Background(), id)
	if tries() != 1 {
		t.Fatalf("tried again after 1 s: %d", tries())
	}
	h.now = h.now.Add(time.Second)
	h.flow.step(context.Background(), id)
	if tries() != 2 {
		t.Fatalf("not tried again after 2 s: %d", tries())
	}
	h.now = h.now.Add(2 * time.Second)
	h.flow.step(context.Background(), id)
	if tries() != 2 {
		t.Fatalf("tried a third time 2 s after the second: %d", tries())
	}
	h.now = h.now.Add(2 * time.Second)
	h.flow.step(context.Background(), id)
	if tries() != 3 {
		t.Fatalf("not tried a third time 4 s after the second: %d", tries())
	}
	// Once it goes, the run is done as any.
	delete(h.tg.fail, "sendPhoto")
	h.now = h.now.Add(retryMost)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageDone || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("stage %s, sent %+v", h.store.stage(id), h.tg.methods("sendPhoto"))
	}
}

// A delivery that still fails an hour after its job ended is given up, so
// a run is never followed for good; its first try after a long outage is
// still made.
func TestAResultThatKeepsFailingIsGivenUp(t *testing.T) {
	h := newHarness(t)
	id := h.doneSlow(t)
	h.files.resultsErr = errBoom
	h.now = h.now.Add(2 * deliverWithin)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGenerating {
		t.Fatalf("given up without a try: stage %s", h.store.stage(id))
	}
	h.now = h.now.Add(retryFirst)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

// Telegram asking to wait is the chat's hold, not a failure: the result
// goes once the hold is over, with no wait of its own on top.
func TestAResultTelegramDefersIsNoFailure(t *testing.T) {
	h := newHarness(t)
	id := h.doneSlow(t)
	h.tg.fail["sendPhoto"] = tooMany(3 * time.Second)
	h.flow.step(context.Background(), id)
	if _, ok := h.flow.retries.Load(id); ok || h.store.stage(id) != db.StageGenerating {
		t.Fatalf("a 429 counted as a failure: stage %s", h.store.stage(id))
	}
}

// A result that went out goes once, even when its record fails: the next
// round records it and sends nothing again. Before, it was sent a second
// time.
func TestAResultWhoseRecordFailedIsNotSentAgain(t *testing.T) {
	h := newHarness(t)
	id := h.doneSlow(t)
	h.store.finishErr = errBoom
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGenerating || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("stage %s, sent %+v", h.store.stage(id), h.tg.methods("sendPhoto"))
	}
	h.now = h.now.Add(followEvery)
	h.flow.step(context.Background(), id)
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 {
		t.Fatalf("sent again: %+v", sent)
	}
	if h.store.stage(id) != db.StageDone || h.store.results[id].FileID != "photo-file" {
		t.Fatalf("stage %s, result %+v", h.store.stage(id), h.store.results[id])
	}
}

// A stop in the middle of a send lets it finish and be recorded, rather
// than cut it short and send it again after the restart.
func TestAStopLetsTheResultInFlightGo(t *testing.T) {
	h := newHarness(t)
	id := h.doneSlow(t)
	ctx, stop := context.WithCancel(context.Background())
	h.tg.onPhoto = stop
	done := make(chan struct{})
	go func() {
		h.flow.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end after the stop")
	}
	if h.store.stage(id) != db.StageDone || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("stage %s, sent %+v", h.store.stage(id), h.tg.methods("sendPhoto"))
	}
}

// After a stop, a run not yet in hand is left for the next process.
func TestAfterAStopNoNewRunIsStarted(t *testing.T) {
	h := newHarness(t)
	id := h.doneSlow(t)
	stop, cancel := context.WithCancel(context.Background())
	cancel()
	h.flow.followUntil(stop, context.Background())
	if h.store.stage(id) != db.StageGenerating || len(h.tg.calls) != 0 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}
