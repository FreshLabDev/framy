// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// The account deleted on the site with a preview open in a chat: the
// preview and its panel go as No takes them, and the run with them. Its
// buttons are no one's in the meantime.
func TestAPreviewGoesWithTheAccount(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	r := run(t, h)
	h.store.setStatus(1, jobs.Cancelled, "")
	h.store.deleteJob(1)
	h.tg.reset()

	msg := tg.Message{MessageID: r.PreviewMessageID, Chat: tg.Chat{ID: r.ChatID, Type: "supergroup"}, Date: 1,
		Photo: []tg.Photo{{FileID: "preview-file"}}}
	if err := h.pressOn(1, 7, actGenerate, msg); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.calls) != len(h.tg.methods("answerCallbackQuery")) || h.store.jobs[1].status != jobs.Cancelled {
		t.Fatalf("a press on a deleted job's preview: %+v", h.tg.calls)
	}
	h.tg.reset()
	h.flow.follow(context.Background())
	if del := h.tg.methods("deleteEphemeralMessage"); len(del) != 1 || del[0].messageID != r.PanelMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if del := h.tg.methods("deleteMessage"); len(del) != 1 || del[0].messageID != r.PreviewMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if _, ok := h.store.runs[1]; ok {
		t.Error("the run stayed")
	}
	h.tg.reset()
	h.flow.follow(context.Background())
	if len(h.tg.calls) != 0 {
		t.Errorf("a second round: %+v", h.tg.calls)
	}
}

// A run deleted on the site the moment it ended, before its result reached
// the chat: the loop goes and no result follows, since the files went with
// the job.
func TestALoopGoesWithItsDeletedJob(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	loop := run(t, h).PreviewMessageID
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.store.deleteJob(id)
	h.flow.follow(context.Background())
	if del := h.tg.methods("deleteMessage"); len(del) != 1 || del[0].messageID != loop || len(h.tg.calls) != 1 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if _, ok := h.store.runs[id]; ok {
		t.Error("the run stayed")
	}
}

// A result already in the chat stays there; its run goes with the job and
// nothing in the chat changes.
func TestADeliveredResultStaysWhenItsJobIsDeleted(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
	h.store.deleteJob(id)
	h.tg.reset()
	h.flow.follow(context.Background())
	if len(h.tg.calls) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if _, ok := h.store.runs[id]; ok {
		t.Error("the run stayed")
	}
}

// A run that moved on in the moment its job was deleted, its result sent as
// the delete went through, is in the chat already: it waits for the look at
// every run, which drops it without a word. The rounds between read only
// the open runs, which an index finds.
func TestARunThatMovedOnAsItsJobWentIsDroppedQuietly(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := h.generating(t)
	h.flow.follow(ctx)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.flow.step(ctx, id)
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
	// The delete went through as the result went out, and left the run.
	h.store.mu.Lock()
	h.store.jobs[id].deleted = true
	h.store.mu.Unlock()
	h.tg.reset()
	h.flow.follow(ctx)
	if _, ok := h.store.runs[id]; !ok {
		t.Fatal("a round of the open runs dropped one that is not open")
	}
	h.now = h.now.Add(sweepEvery)
	h.flow.follow(ctx)
	if _, ok := h.store.runs[id]; ok {
		t.Error("the look at every run left it")
	}
	if len(h.tg.calls) != 0 {
		t.Errorf("calls = %+v", h.tg.calls)
	}
}

// A guest answer cannot be deleted: a preview of a deleted job becomes one
// word, a loop keeps its clip with that word and no buttons.
func TestAGuestRunOfADeletedJobSaysDeleted(t *testing.T) {
	deleted := i18n.T("en", "guest.deleted")
	t.Run("preview", func(t *testing.T) {
		h := guestHarness(t)
		r := h.guestRun(t, "make it snow")
		h.store.setStatus(1, jobs.Cancelled, "")
		h.store.deleteJob(1)
		h.tg.reset()
		h.flow.follow(context.Background())
		texts := h.tg.methods("editInlineMessageText")
		if len(texts) != 1 || texts[0].inline != r.InlineMessageID || texts[0].text != deleted || texts[0].markup != nil ||
			texts[0].preview != "" || len(h.tg.calls) != 1 {
			t.Fatalf("calls = %+v", h.tg.calls)
		}
	})
	t.Run("loop", func(t *testing.T) {
		h := guestHarness(t)
		r := h.guestRun(t, "make it snow")
		if err := h.pressInline(1, 7, actGenerate, r.InlineMessageID); err != nil {
			t.Fatal(err)
		}
		h.store.setStatus(1, jobs.Failed, jobs.Unavailable)
		h.store.deleteJob(1)
		h.tg.reset()
		h.flow.follow(context.Background())
		caps := h.tg.methods("editInlineMessageCaption")
		if len(caps) != 1 || caps[0].inline != r.InlineMessageID || caps[0].text != deleted || caps[0].markup != nil || len(h.tg.calls) != 1 {
			t.Fatalf("calls = %+v", h.tg.calls)
		}
		if _, ok := h.store.runs[1]; ok {
			t.Error("the run stayed")
		}
	})
}
