// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// busy takes every slot, as commands being worked would.
func (h *harness) busy() {
	for range cap(h.flow.slot) {
		h.flow.slot <- struct{}{}
	}
}

// A command still waiting for a slot when framy stops is not worked, and
// Telegram gives its update to no one again: it is told to come back rather
// than met with silence, as it was before.
func TestACommandAStopLeavesWaitingIsToldToComeBack(t *testing.T) {
	h := newHarness(t)
	h.busy()
	// Whether it waits for a slot by the time the stop comes or comes to
	// the slots after, it finds none.
	h.flow.Command(context.Background(), command("/img a fox"), CommandImage, "a fox")
	h.flow.markStopping()
	h.flow.Wait()
	sent := h.tg.methods("sendMessage")
	if len(sent) != 1 || sent[0].text != i18n.T("en", "run.restarting") || sent[0].messageID != 10 || len(h.store.jobs) != 0 {
		t.Fatalf("jobs %d, calls %+v", len(h.store.jobs), h.tg.calls)
	}
}

// One that comes after the stop is worked while a slot is free, within the
// bot's grace, and told to come back when none is.
func TestACommandAfterAStopIsWorkedWhileASlotIsFree(t *testing.T) {
	h := newHarness(t)
	h.flow.markStopping()
	h.flow.Command(context.Background(), command("/img a fox"), CommandImage, "a fox")
	h.flow.Wait()
	if len(h.store.jobs) != 1 || len(h.tg.methods("sendRichMessage")) != 1 {
		t.Fatalf("jobs %d, calls %+v", len(h.store.jobs), h.tg.calls)
	}

	h = newHarness(t)
	h.flow.markStopping()
	h.busy()
	m := guestReply("gq-1")
	h.flow.Guest(context.Background(), m, "a fox")
	h.flow.Wait()
	if len(h.tg.guestAnswers) != 1 || len(h.store.jobs) != 0 {
		t.Fatalf("jobs %d, answers %+v", len(h.store.jobs), h.tg.guestAnswers)
	}
	a, ok := h.tg.guestAnswers[0].(tg.InlineQueryResultArticle)
	if !ok || a.InputMessageContent.(tg.InputTextMessageContent).MessageText != i18n.T("en", "run.restarting") {
		t.Fatalf("answer %+v", h.tg.guestAnswers[0])
	}
}
