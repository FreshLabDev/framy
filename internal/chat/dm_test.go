// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// dmGenerating makes run 1 of person 7 in their DM and presses Generate on
// its preview.
func (h *harness) dmGenerating(t *testing.T) int64 {
	t.Helper()
	m := command("/img a fox")
	m.Chat = dm
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if err := h.pressOn(1, 7, actGenerate, tg.Message{MessageID: h.store.runs[1].PreviewMessageID, Chat: dm, Date: 1}); err != nil {
		t.Fatal(err)
	}
	return 1
}

func isStop(kb *tg.InlineKeyboardMarkup, id int64) bool {
	return kb != nil && len(kb.InlineKeyboard) == 1 && len(kb.InlineKeyboard[0]) == 1 && kb.InlineKeyboard[0][0].CallbackData == callback(id, actStop)
}

// A DM's run is one message, as a group's: the loop carries the time and
// Stop. A draft beside it would say the same a second time.
func TestADMRunIsTheLoopWithStopAndTheTime(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].messageID != loop || !strings.HasPrefix(edits[0].file, "animation ") || !isStop(edits[0].markup, id) {
		t.Fatalf("loop = %+v", edits)
	}
	h.tg.reset()
	h.store.setStatus(id, jobs.Running, "")
	h.now = h.now.Add(11 * time.Second)
	h.flow.step(context.Background(), id)
	caps := h.tg.methods("editMessageCaption")
	if len(caps) != 1 || caps[0].messageID != loop || !strings.Contains(caps[0].text, "0:11") || !isStop(caps[0].markup, id) {
		t.Fatalf("captions = %+v", caps)
	}
	for _, c := range h.tg.calls {
		if c.method != "editMessageCaption" && c.method != "sendChatAction" {
			t.Errorf("progress sent %s", c.method)
		}
	}
}

// A quick result takes the loop's place in a DM too.
func TestADMResultTakesTheLoopsPlace(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.tg.reset()
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(12 * time.Second)
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].messageID != loop || edits[0].file != "photo upload:framy.png" || len(h.tg.methods("sendPhoto")) != 0 {
		t.Fatalf("edits %+v, calls %+v", edits, h.tg.calls)
	}
}

// So does an error card.
func TestADMFailureTakesTheLoopsPlace(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.tg.reset()
	h.store.setStatus(id, jobs.Failed, "provider")
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].messageID != loop || len(h.tg.methods("sendPhoto")) != 0 || len(h.tg.methods("deleteMessage")) != 0 {
		t.Fatalf("edits %+v, calls %+v", edits, h.tg.calls)
	}
}
