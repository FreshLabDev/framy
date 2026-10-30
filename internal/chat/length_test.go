// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
)

func withKey(h *harness) {
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
}

// longClip is /edit on a clip of secs, its preview sent and the calls
// forgotten.
func longClip(t *testing.T, secs float64, setup func(*harness)) *harness {
	t.Helper()
	h := videoHarness(t)
	h.clips.seconds = secs
	if setup != nil {
		setup(h)
	}
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return h
}

// cardButtons are the labels and the data of the latest keyboard on the
// preview: as sent, or as last edited.
func cardButtons(t *testing.T, h *harness) (text, data string) {
	t.Helper()
	calls := h.tg.cards()
	if len(calls) == 0 {
		t.Fatal("no card")
	}
	kb := calls[len(calls)-1].markup.InlineKeyboard
	switch len(kb) {
	case 1:
		return "", ""
	case 2:
		if len(kb[1]) == 1 {
			return kb[1][0].Text, kb[1][0].CallbackData
		}
	}
	t.Fatalf("keyboard = %v", labels(calls[len(calls)-1].markup))
	return "", ""
}

func TestAFreeEditOfALongClipOffersAllOfItOnTheKey(t *testing.T) {
	h := videoHarness(t)
	h.clips.seconds = 12.4
	withKey(h)
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	text, data := cardButtons(t, h)
	if text != i18n.T("en", "edit.key_whole", "n", "12") || data != callback(1, actKeyClip) {
		t.Fatalf("button = %q %q", text, data)
	}
	if run := h.store.runs[1]; run.ClipFileID != "clip" || run.ClipBytes != 3<<20 || run.ClipSeconds != 12.4 {
		t.Fatalf("run = %+v", run)
	}
	h.tg.reset()

	if err := h.press(1, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != i18n.T("en", "edit.taking", "n", "12") {
		t.Fatalf("answers = %v", h.tg.answers)
	}
	// The clip is read again and kept whole.
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "clip" {
		t.Fatalf("getFile = %+v", got)
	}
	if len(h.files.cuts) != 2 || h.files.cuts[1] != nil {
		t.Fatalf("cuts = %v", h.files.cuts)
	}
	job := h.store.jobs[1]
	if job.n.Free || len(job.n.Inputs) != 1 || job.n.Inputs[0] != 2 || job.n.Estimate == nil || job.n.Estimate.USD <= 0 {
		t.Fatalf("job = %+v", job.n)
	}
	if len(h.queue.revised) != 1 || h.queue.revised[0].Inputs[0] != 2 {
		t.Fatalf("revised = %+v", h.queue.revised)
	}
	if run := h.store.runs[1]; run.Note != "" {
		t.Errorf("note = %q", run.Note)
	}
	// The card is drawn again on the key, with the free 5 s offered back.
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 1 || edits[0].messageID != h.store.runs[1].PreviewMessageID {
		t.Fatalf("edits = %+v", edits)
	}
	p := h.renderer.previews[len(h.renderer.previews)-1]
	if p.Free || p.Chips[0] != "0:12" || p.Label != i18n.T("en", "card.key", "provider", "OpenRouter") {
		t.Errorf("card = %+v", p)
	}
	text, data = cardButtons(t, h)
	if text != i18n.T("en", "edit.free_first", "n", "5") || data != callback(1, actFreeClip) {
		t.Fatalf("button = %q %q", text, data)
	}
	h.tg.reset()

	// And back.
	if err := h.press(1, 7, actFreeClip); err != nil {
		t.Fatal(err)
	}
	if len(h.files.cuts) != 3 || h.files.cuts[2] == nil || *h.files.cuts[2] != (media.Cut{Seconds: 5}) {
		t.Fatalf("cuts = %v", h.files.cuts)
	}
	if job := h.store.jobs[1]; !job.n.Free || job.n.Inputs[0] != 3 {
		t.Fatalf("job = %+v", job.n)
	}
	if run := h.store.runs[1]; run.Note != noteClipCut {
		t.Errorf("note = %q", run.Note)
	}
	if text, data := cardButtons(t, h); text != i18n.T("en", "edit.key_whole", "n", "12") || data != callback(1, actKeyClip) {
		t.Fatalf("button = %q %q", text, data)
	}
}

// Past what the model takes, the key offers its first 15 s.
func TestAClipLongerThanTheModelTakesOffersItsFirstSeconds(t *testing.T) {
	h := longClip(t, 40, withKey)
	if err := h.press(1, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	if len(h.files.cuts) != 2 || h.files.cuts[1] == nil || *h.files.cuts[1] != (media.Cut{Seconds: 15}) {
		t.Fatalf("cuts = %v", h.files.cuts)
	}
	if run := h.store.runs[1]; run.Note != noteClipCut {
		t.Errorf("note = %q", run.Note)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != i18n.T("en", "edit.taking", "n", "15") {
		t.Fatalf("answers = %v", h.tg.answers)
	}
}

func TestTheKeyLengthWithoutAKeyAsksForOne(t *testing.T) {
	h := videoHarness(t)
	h.clips.seconds = 40
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	text, data := cardButtons(t, h)
	if text != "🔑 "+i18n.T("en", "edit.key_first", "n", "15") || data != callback(1, actKeyClip) {
		t.Fatalf("button = %q %q", text, data)
	}
	h.tg.reset()
	if err := h.press(1, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+i18n.T("en", "panel.key_needed") {
		t.Fatalf("answers = %v", h.tg.answers)
	}
	if len(h.tg.calls) != 0 || len(h.queue.revised) != 0 || len(h.files.cuts) != 1 {
		t.Fatalf("calls %+v, revised %d, cuts %d", h.tg.calls, len(h.queue.revised), len(h.files.cuts))
	}
}

func TestAClipTheFreeVideoTakesWholeHasNoOtherLength(t *testing.T) {
	for _, secs := range []float64{4, 5.04} {
		h := longClip(t, secs, withKey)
		if err := h.press(1, 7, actKeyClip); err != nil {
			t.Fatal(err)
		}
		if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 {
			t.Fatalf("%g s: revised %d, calls %+v", secs, len(h.queue.revised), h.tg.calls)
		}
	}
}

// Past the week's free video the key takes what it can, and there is
// nothing to offer.
func TestAnEditPastTheFreeVideoHasNoOtherLength(t *testing.T) {
	for _, secs := range []float64{12, 40} {
		h := videoHarness(t)
		h.clips.seconds = secs
		h.flow.cfg.Allowance = fakeAllowance{left: 5, videosUsed: 1}
		withKey(h)
		if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
			t.Fatal(err)
		}
		if text, _ := cardButtons(t, h); text != "" {
			t.Errorf("%g s: button %q", secs, text)
		}
	}
}

// The queue may refuse the free video after the clip was kept at 5 s: the
// run goes on the key, which takes the whole clip at a press.
func TestARunTheQueueMovedToTheKeyOffersTheKeyLength(t *testing.T) {
	h := videoHarness(t)
	h.clips.seconds = 12.4
	h.queue.refuse = &free.ClosedError{}
	withKey(h)
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || job.n.Free {
		t.Fatalf("job = %+v", job)
	}
	if text, data := cardButtons(t, h); text != i18n.T("en", "edit.key_whole", "n", "12") || data != callback(1, actKeyClip) {
		t.Fatalf("button = %q %q", text, data)
	}
	h.tg.reset()
	if err := h.press(1, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	if len(h.files.cuts) != 2 || h.files.cuts[1] != nil || h.store.jobs[1].n.Free {
		t.Fatalf("cuts %v, job %+v", h.files.cuts, h.store.jobs[1].n)
	}
	// The meter still says a video is left, but the whole clip is taken.
	if text, _ := cardButtons(t, h); text != i18n.T("en", "edit.free_first", "n", "5") {
		t.Fatalf("button = %q", text)
	}
}

// The free video can go between the card and the press: the run stays on
// the key, as its card shows.
func TestAFreeLengthTheQueueRefusesLeavesTheRunOnTheKey(t *testing.T) {
	h := longClip(t, 12.4, withKey)
	if err := h.press(1, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.queue.refuse = &free.LimitError{Kind: catalog.Video, Limit: 1}
	if err := h.press(1, 7, actFreeClip); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job.n.Free || job.n.Inputs[0] != 2 {
		t.Fatalf("job = %+v", job.n)
	}
	if len(h.tg.methods("editMessageRich")) != 0 || h.store.runs[1].Note != "" {
		t.Fatalf("calls %+v, note %q", h.tg.calls, h.store.runs[1].Note)
	}
}

// Old or forged data changes nothing: the free length on a free run, a
// length on a photo run, and a length after Generate.
func TestALengthTheCardDoesNotOfferChangesNothing(t *testing.T) {
	h := longClip(t, 12.4, withKey)
	if err := h.press(1, 7, actFreeClip); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 || h.tg.answers[0] != "" {
		t.Fatalf("revised %d, calls %+v, answers %v", len(h.queue.revised), h.tg.calls, h.tg.answers)
	}

	img := newHarness(t)
	id := img.preview(t)
	if err := img.press(id, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	if len(img.queue.revised) != 0 || len(img.tg.calls) != 0 {
		t.Fatalf("photo run: revised %d, calls %+v", len(img.queue.revised), img.tg.calls)
	}

	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.press(1, 7, actKeyClip); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != 0 || len(h.files.cuts) != 1 {
		t.Fatalf("after Generate: revised %d, cuts %d", len(h.queue.revised), len(h.files.cuts))
	}
}
