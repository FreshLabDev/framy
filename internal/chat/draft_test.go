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

// dmPreview makes run 1 of person 7 in their DM, its preview shown.
func (h *harness) dmPreview(t *testing.T) int64 {
	t.Helper()
	m := command("/img a fox")
	m.Chat = dm
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return 1
}

// dmGenerate presses Generate on the DM's preview.
func (h *harness) dmGenerate(t *testing.T, id int64) {
	t.Helper()
	if err := h.pressOn(id, 7, actGenerate, tg.Message{MessageID: h.store.runs[id].PreviewMessageID, Chat: dm, Date: 1}); err != nil {
		t.Fatal(err)
	}
}

// dmGenerating makes a DM run whose job is queued, its draft sent.
func (h *harness) dmGenerating(t *testing.T) int64 {
	t.Helper()
	id := h.dmPreview(t)
	h.dmGenerate(t, id)
	h.tg.reset()
	return id
}

func isStop(kb *tg.InlineKeyboardMarkup, id int64) bool {
	return kb != nil && len(kb.InlineKeyboard) == 1 && len(kb.InlineKeyboard[0]) == 1 && kb.InlineKeyboard[0][0].CallbackData == callback(id, actStop)
}

// In a DM Generate sends a draft with Stop, named by the job, and the loop
// has no Stop of its own.
func TestADMRunShowsADraftWithStop(t *testing.T) {
	h := newHarness(t)
	id := h.dmPreview(t)
	h.dmGenerate(t, id)
	drafts := h.tg.methods("sendMessageDraft")
	if len(drafts) != 1 || drafts[0].chatID != 7 || drafts[0].messageID != id || drafts[0].action != "stop" ||
		drafts[0].text != generatingLine("en", "MAI Image 2.6 Flash", 0) {
		t.Fatalf("drafts = %+v", drafts)
	}
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || !strings.HasPrefix(edits[0].file, "animation") || edits[0].markup != nil {
		t.Fatalf("loop = %+v", edits)
	}

	// Every tick the draft goes again with the time, and the loop is left be.
	h.tg.reset()
	h.now = h.now.Add(captionEvery)
	h.flow.step(context.Background(), id)
	if d := h.tg.methods("sendMessageDraft"); len(d) != 1 || !strings.Contains(d[0].text, "0:10") || d[0].action != "stop" {
		t.Fatalf("drafts = %+v", d)
	}
	if c := h.tg.methods("editMessageCaption"); len(c) != 0 {
		t.Fatalf("captions = %+v", c)
	}

	// A draft Telegram refuses puts Stop and the time back on the loop.
	h.tg.reset()
	h.tg.fail["sendMessageDraft"] = errBoom
	h.now = h.now.Add(captionEvery)
	h.flow.step(context.Background(), id)
	if c := h.tg.methods("editMessageCaption"); len(c) != 1 || !strings.Contains(c[0].text, "0:20") || !isStop(c[0].markup, id) {
		t.Fatalf("captions = %+v", c)
	}

	// Once the draft goes again the loop sheds them, once.
	delete(h.tg.fail, "sendMessageDraft")
	for i, want := range []int{1, 0} {
		h.tg.reset()
		h.now = h.now.Add(captionEvery)
		h.flow.step(context.Background(), id)
		c := h.tg.methods("editMessageCaption")
		if len(h.tg.methods("sendMessageDraft")) != 1 || len(c) != want {
			t.Fatalf("tick %d: calls %+v", i, h.tg.calls)
		}
		if want == 1 && (c[0].markup != nil || strings.Contains(c[0].text, ":")) {
			t.Fatalf("tick %d: caption %+v", i, c[0])
		}
	}
}

// Where Telegram takes no draft, the DM's loop keeps Stop, as in a group.
func TestADMRunWithoutADraftKeepsStopOnTheLoop(t *testing.T) {
	h := newHarness(t)
	id := h.dmPreview(t)
	h.tg.fail["sendMessageDraft"] = errBoom
	h.dmGenerate(t, id)
	if edits := h.tg.methods("editMessageMedia"); len(edits) != 1 || !isStop(edits[0].markup, id) {
		t.Fatalf("loop = %+v", edits)
	}
}

// A group's run has no draft: Telegram streams drafts into private chats.
func TestAGroupRunHasNoDraft(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.now = h.now.Add(captionEvery)
	h.flow.step(context.Background(), id)
	if d := h.tg.methods("sendMessageDraft"); len(d) != 0 {
		t.Fatalf("drafts = %+v", d)
	}
}

// A DM's result, however quick, is a new message, which takes the draft's
// place; the loop goes.
func TestADMResultTakesTheDraftsPlace(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(5 * time.Second)
	h.flow.step(context.Background(), id)
	if e := h.tg.methods("editMessageMedia"); len(e) != 0 {
		t.Fatalf("put in place: %+v", e)
	}
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || sent[0].chatID != 7 || sent[0].messageID != 10 {
		t.Fatalf("sent = %+v", sent)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != loop {
		t.Fatalf("deletes = %+v", d)
	}
}

// A DM's failure is a new message too.
func TestADMFailureTakesTheDraftsPlace(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.store.setStatus(id, jobs.Failed, jobs.Internal)
	h.flow.step(context.Background(), id)
	if e := h.tg.methods("editMessageMedia"); len(e) != 0 {
		t.Fatalf("put in place: %+v", e)
	}
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || sent[0].messageID != 10 || len(h.renderer.cards) != 1 {
		t.Fatalf("sent = %+v, cards %v", sent, h.renderer.cards)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != loop {
		t.Fatalf("deletes = %+v", d)
	}
	if h.store.stage(id) != db.StageFailed {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

func (h *harness) stopDraft(chatID, draftID int64) {
	h.flow.StopDraft(context.Background(), &tg.MessageGenerationStopped{Chat: tg.Chat{ID: chatID, Type: tg.ChatPrivate}, DraftID: draftID})
	h.flow.Wait()
}

// Stop on the draft of a queued job cancels it, and the loop goes.
func TestStopOnADraftCancelsAQueuedJob(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.stopDraft(7, id)
	if h.store.jobs[id].status != jobs.Cancelled || h.store.stage(id) != db.StageGone || h.store.runs[id].Stopped {
		t.Fatalf("status %s, run %+v", h.store.jobs[id].status, h.store.runs[id])
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != loop {
		t.Fatalf("deletes = %+v", d)
	}
	if s := h.tg.methods("sendMessage"); len(s) != 0 {
		t.Fatalf("told %+v", s)
	}
}

// Stop on the draft of a running image keeps the result in the history,
// out of the DM, and says so there.
func TestStopOnADraftOfARunningImage(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	h.store.setStatus(id, jobs.Running, "")
	h.queue.cancelErr = jobs.ErrRunning
	h.stopDraft(7, id)
	if !h.store.runs[id].Stopped || h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
		t.Fatalf("run %+v, calls %+v", h.store.runs[id], h.tg.calls)
	}
	if s := h.tg.methods("sendMessage"); len(s) != 1 || s[0].chatID != 7 || s[0].messageID != 10 || s[0].text != i18n.T("en", "run.stopped") {
		t.Fatalf("told %+v", s)
	}
	// The job ends done; the DM gets nothing of it.
	h.tg.reset()
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.flow.step(context.Background(), id)
	if len(h.tg.calls) != 0 {
		t.Fatalf("posted %+v", h.tg.calls)
	}
}

// A stop names a draft of its own chat; any other changes nothing.
func TestAStopOnADraftIsTheDMsOwn(t *testing.T) {
	h := newHarness(t)
	group := h.generating(t)
	direct := int64(2)
	m := command("/img a wolf")
	m.Chat, m.MessageID = dm, 20
	if err := h.command(m, CommandImage, "a wolf"); err != nil {
		t.Fatal(err)
	}
	h.dmGenerate(t, direct)
	h.tg.reset()
	for _, c := range []struct{ chat, draft int64 }{
		{8, direct},   // another person's DM
		{7, group},    // a group's run has no draft
		{7, 99},       // no such run
		{7, 0},        // no draft
		{-100, group}, // not a DM
	} {
		h.stopDraft(c.chat, c.draft)
	}
	if len(h.tg.calls) != 0 || len(h.queue.cancelled) != 0 || h.store.stage(group) != db.StageGenerating || h.store.stage(direct) != db.StageGenerating {
		t.Fatalf("calls %+v, cancelled %v", h.tg.calls, h.queue.cancelled)
	}
}
