// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// edit works an edit of a message as the bot hands it over.
func (h *harness) edit(m *tg.Message, command, prompt string) error {
	return h.flow.edited(context.Background(), m, command, prompt)
}

// words are the job's prompt as the queue has it.
func (h *harness) words(jobID int64) string {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return h.store.jobs[jobID].n.Prompt
}

// A typo fixed in the command reaches the preview while it waits for
// Generate: the card shows the new words and their price, and the draft
// runs them. Words that did not change, or were taken out, change nothing.
func TestAnEditOfTheCommandChangesThePreviewsWords(t *testing.T) {
	h := newHarness(t)
	if err := h.command(photoReply("/img a fxo in the snow"), CommandImage, "a fxo in the snow"); err != nil {
		t.Fatal(err)
	}
	preview := run(t, h).PreviewMessageID
	h.tg.reset()

	if err := h.edit(photoReply("/img a fox in the snow"), CommandImage, "a fox in the snow"); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 1 || edits[0].messageID != preview || !strings.Contains(edits[0].text, "<blockquote>a fox in the snow</blockquote>") {
		t.Fatalf("edits = %+v", edits)
	}
	if got := h.words(1); got != "a fox in the snow" {
		t.Fatalf("the draft runs %q", got)
	}
	if j := h.store.jobs[1]; j.n.Estimate == nil || j.status != jobs.Draft {
		t.Fatalf("draft = %+v", j)
	}

	h.tg.reset()
	for _, words := range []string{"a fox in the snow", "  ", ""} {
		if err := h.edit(photoReply("/img "+words), CommandImage, words); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.tg.calls) != 0 || h.words(1) != "a fox in the snow" {
		t.Fatalf("calls %+v, words %q", h.tg.calls, h.words(1))
	}

	// Generate runs the words the card showed.
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if j := h.store.jobs[1]; j.status != jobs.Queued || j.n.Prompt != "a fox in the snow" {
		t.Fatalf("queued = %+v", j)
	}
}

// The mention in a group and a plain message of a DM are edited as /img is.
func TestAnEditOfAMentionOrADirectMessageChangesTheWords(t *testing.T) {
	h := newHarness(t)
	if err := h.flow.byName(context.Background(), command("@framyin_bot a fxo"), "a fxo"); err != nil {
		t.Fatal(err)
	}
	if err := h.edit(command("@framyin_bot a fox"), "", "a fox"); err != nil {
		t.Fatal(err)
	}
	if h.words(1) != "a fox" || len(h.tg.methods("editMessageRich")) != 1 {
		t.Fatalf("words %q, calls %+v", h.words(1), h.tg.calls)
	}

	h = dmHarness(t)
	q := h.ask(t, said("a fxo"))
	if err := h.answer(q, "d:10:i", 7, nil); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.edit(said("a fox"), "", "a fox"); err != nil {
		t.Fatal(err)
	}
	if h.words(1) != "a fox" || len(h.tg.methods("editMessageRich")) != 1 {
		t.Fatalf("words %q, calls %+v", h.words(1), h.tg.calls)
	}
}

// A question of a DM waiting for its answer asks about the message as it
// reads now: the answer starts the run with the words the person left.
func TestAnEditBeforeTheAnswerIsWhatTheAnswerTakes(t *testing.T) {
	h := dmHarness(t)
	q := h.ask(t, photoSaid("a fxo"))
	if err := h.edit(photoSaid("a fox"), "", "a fox"); err != nil {
		t.Fatal(err)
	}
	// Words taken out leave the ones the question has.
	if err := h.edit(photoSaid(""), "", ""); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.calls) != 1 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if err := h.answer(q, "d:10:i", 7, nil); err != nil {
		t.Fatal(err)
	}
	if h.words(1) != "a fox" {
		t.Fatalf("the run took %q", h.words(1))
	}
}

// An edit asks nothing of a run past its preview, of a preview only the
// requester sees, of another person's or another message's run, or for the
// other command; and it never starts a run.
func TestAnEditLeavesOtherRunsAlone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(h *harness)
		edit    *tg.Message
		command string
	}{
		{name: "after Generate", prepare: func(h *harness) {
			if err := h.press(1, 7, actGenerate); err != nil {
				t.Fatal(err)
			}
		}, edit: photoReply("/img a fox"), command: CommandImage},
		{name: "the other command", edit: photoReply("/edit a fox"), command: CommandVideo},
		{name: "another message", edit: func() *tg.Message {
			m := photoReply("/img a fox")
			m.MessageID = 11
			return m
		}(), command: CommandImage},
		{name: "another person", edit: func() *tg.Message {
			m := photoReply("/img a fox")
			m.From = &tg.User{ID: 8, LanguageCode: "en"}
			return m
		}(), command: CommandImage},
	} {
		h := newHarness(t)
		if err := h.command(photoReply("/img a fxo"), CommandImage, "a fxo"); err != nil {
			t.Fatal(err)
		}
		if tc.prepare != nil {
			tc.prepare(h)
		}
		h.tg.reset()
		if err := h.edit(tc.edit, tc.command, "a fox"); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(h.tg.calls) != 0 || h.words(1) != "a fxo" {
			t.Fatalf("%s: calls %+v, words %q", tc.name, h.tg.calls, h.words(1))
		}
	}

	// A message that started nothing starts nothing when edited.
	h := newHarness(t)
	if err := h.edit(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.calls) != 0 || len(h.store.jobs) != 0 {
		t.Fatalf("calls %+v, jobs %d", h.tg.calls, len(h.store.jobs))
	}
}

// Where framy is an admin the preview is the requester's own, and an edit
// sends it to them anew with the new words, as a press does: an ephemeral
// rich message is not edited (resendRich).
func TestAnEditSendsTheRequestersOwnPreviewAnew(t *testing.T) {
	h, r := adminPreview(t)
	if err := h.edit(photoReply("/img make it snow"), CommandImage, "make it snow"); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].ephemeral == nil || sent[0].ephemeral.ReceiverUserID != 7 || !strings.Contains(sent[0].text, "<blockquote>make it snow</blockquote>") {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if del := h.tg.methods("deleteEphemeralMessage"); len(del) != 1 || del[0].messageID != r.PreviewMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if got := run(t, h); got.PreviewMessageID == r.PreviewMessageID || h.words(1) != "make it snow" {
		t.Fatalf("run = %+v, words %q", got, h.words(1))
	}
}

// Generate never runs words the preview does not show. A card Telegram
// refused, or one that never reached it, leaves the draft its words; one
// that may have reached it gives the draft the new ones; and a draft that
// could not take them gets its card drawn back.
func TestAnEditTheCardDoesNotShowIsNotRun(t *testing.T) {
	refused := &tg.APIError{Method: "editMessageText", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: message to edit not found"}
	for _, tc := range []struct {
		name  string
		fail  error
		words string
	}{
		{"refused", refused, "a fxo"},
		{"no answer", errors.New("telegram transport failed: EOF"), "a fox"},
	} {
		h := newHarness(t)
		if err := h.command(photoReply("/img a fxo"), CommandImage, "a fxo"); err != nil {
			t.Fatal(err)
		}
		h.tg.fail["editMessageRich"] = tc.fail
		if err := h.edit(photoReply("/img a fox"), CommandImage, "a fox"); err == nil {
			t.Fatalf("%s: no error", tc.name)
		}
		if h.words(1) != tc.words {
			t.Fatalf("%s: the draft runs %q", tc.name, h.words(1))
		}
	}

	h := newHarness(t)
	if err := h.command(photoReply("/img a fxo"), CommandImage, "a fxo"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.queue.repromptErr = errBoom
	if err := h.edit(photoReply("/img a fox"), CommandImage, "a fox"); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 2 || !strings.Contains(edits[0].text, "<blockquote>a fox</blockquote>") || !strings.Contains(edits[1].text, "<blockquote>a fxo</blockquote>") {
		t.Fatalf("edits = %+v", edits)
	}
	if h.words(1) != "a fxo" {
		t.Fatalf("the draft runs %q", h.words(1))
	}

	// A draft whose ten minutes ran out meanwhile is left to go as they do.
	h = newHarness(t)
	if err := h.command(photoReply("/img a fxo"), CommandImage, "a fxo"); err != nil {
		t.Fatal(err)
	}
	h.queue.repromptErr = jobs.ErrNotDraft
	if err := h.edit(photoReply("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if r := run(t, h); r.Stage != db.StagePreview {
		t.Fatalf("stage = %s", r.Stage)
	}
}

// A word changed where the card does not quote the prompt (it cuts it) draws
// the card as it was, and Telegram says it is not modified. The card shows
// all that the words give it, so the draft takes them all the same.
func TestAnEditPastWhatTheCardQuotesIsTaken(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("a fox in the snow ", 60)
	if err := h.command(photoReply("/img "+long), CommandImage, long); err != nil {
		t.Fatal(err)
	}
	h.tg.fail["editMessageRich"] = &tg.APIError{Method: "editMessageText", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"}
	edited := long + "and a wolf"
	if err := h.edit(photoReply("/img "+edited), CommandImage, edited); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := h.words(1); got != edited {
		t.Fatalf("the draft runs %q", got)
	}
}
