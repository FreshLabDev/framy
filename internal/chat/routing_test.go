// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/media"
)

// These go through the doors the bot opens, Owns and the background
// entries, rather than the steps behind them that the other tests call:
// a door that sent a press or a command the wrong way would leave those
// tests green and the buttons dead.

// The flow takes every button it puts on a run and both answers to the DM's
// question, and nothing else: the bot's panels are "m:<owner>:<action>".
func TestTheFlowOwnsItsButtonsAndNoOthers(t *testing.T) {
	for _, action := range []string{actGenerate, actNo, actStop, actModel, actSettings, actBack, actPickModel, actPickSetting,
		actFamily, actLocked, actVideo, actImage, actKeyClip, actFreeClip, actAnimate, actAgain, actOriginal, actRetry, actOther, actQuiet} {
		if data := callback(4503599627370495, action); !Owns(data) {
			t.Errorf("a run's %q is not the flow's", data)
		}
	}
	for _, answer := range []string{askImage, askVideo} {
		if data := askPrefix + "10:" + answer; !Owns(data) {
			t.Errorf("the question's %q is not the flow's", data)
		}
	}
	for _, data := range []string{"m:7:home", "m:7:lang:uk", "m:7:close", "", "j", "d", "jx:1:go", "J:1:go"} {
		if Owns(data) {
			t.Errorf("the flow takes %q", data)
		}
	}
	if h := newHarness(t); !h.flow.Owns(callback(1, actGenerate)) || h.flow.Owns("m:7:home") {
		t.Error("Flow.Owns is not Owns")
	}
}

// pressed is a press as Telegram sends it, through Callback, worked to its
// end.
func (h *harness) pressed(cq *tg.CallbackQuery) {
	h.flow.Callback(context.Background(), cq)
	h.flow.Wait()
}

// Generate pressed on a group's preview starts the run.
func TestAPressOnAPreviewGoesToTheRun(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	run := h.runOf(t, id)
	msg := tg.Message{Chat: group, Date: 1}
	if run.PreviewEphemeral {
		msg.EphemeralMessageID = run.PreviewMessageID
	} else {
		msg.MessageID = run.PreviewMessageID
	}
	h.pressed(&tg.CallbackQuery{ID: "cq", From: tg.User{ID: 7, LanguageCode: "en"}, Data: callback(id, actGenerate), Message: &msg})
	if h.store.stage(id) != db.StageGenerating {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

// Image under the DM's question makes the run it asks about.
func TestAnAnswerToTheQuestionGoesToTheQuestion(t *testing.T) {
	h := dmHarness(t)
	q := h.ask(t, said("a fox in the snow"))
	h.tg.reset()
	h.pressed(&tg.CallbackQuery{ID: "cq", From: tg.User{ID: 7, LanguageCode: "en"}, Data: askPrefix + "10:" + askImage,
		Message: &tg.Message{MessageID: q, Chat: dm, Date: 1, Text: "?"}})
	if job := h.store.jobs[1]; job == nil || job.n.Prompt != "a fox in the snow" || len(h.tg.methods("sendRichMessage")) != 1 {
		t.Fatalf("jobs %+v, calls %+v", h.store.jobs, h.tg.calls)
	}
}

// A press on a guest run's answer has no message, only the inline
// message's id, and goes to the run; a press with neither, or one of the
// DM's question from an inline message, which framy never puts there, is
// only answered.
func TestAPressOnAnInlineMessageGoesToItsGuestRunOnly(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	h.tg.reset()
	h.pressed(&tg.CallbackQuery{ID: "cq", From: tg.User{ID: 7, LanguageCode: "en"}, Data: callback(run.JobID, actGenerate), InlineMessageID: run.InlineMessageID})
	if h.store.stage(run.JobID) != db.StageGenerating || len(h.tg.methods("editInlineMessageMedia")) != 1 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(run.JobID), h.tg.calls)
	}

	for name, cq := range map[string]*tg.CallbackQuery{
		"no message":         {ID: "cq", From: tg.User{ID: 7}, Data: callback(run.JobID, actStop)},
		"a question's press": {ID: "cq", From: tg.User{ID: 7}, Data: askPrefix + "10:" + askImage, InlineMessageID: run.InlineMessageID},
	} {
		h.tg.reset()
		h.pressed(cq)
		if len(h.tg.calls) != 0 || len(h.tg.answers) != 1 || h.tg.answers[0] != "" || h.store.stage(run.JobID) != db.StageGenerating {
			t.Errorf("%s: stage %s, calls %+v, answers %q", name, h.store.stage(run.JobID), h.tg.calls, h.tg.answers)
		}
	}
}

// /img through Command makes its preview, with the prompt trimmed; a
// message with no sender is nobody's run.
func TestACommandIsWorkedInTheBackground(t *testing.T) {
	h := newHarness(t)
	h.flow.Command(context.Background(), command("/img   a fox  "), CommandImage, "   a fox  ")
	h.flow.Wait()
	if job := h.store.jobs[1]; job == nil || job.n.Prompt != "a fox" || len(h.tg.methods("sendRichMessage")) != 1 {
		t.Fatalf("jobs %+v, calls %+v", h.store.jobs, h.tg.calls)
	}

	h = newHarness(t)
	m := command("/img a fox")
	m.From = nil
	h.flow.Command(context.Background(), m, CommandImage, "a fox")
	h.flow.Wait()
	if len(h.store.jobs) != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("jobs %+v, calls %+v", h.store.jobs, h.tg.calls)
	}
}

// "@framyin_bot a fox" through Mention is /img a fox; the mention alone is
// told what to write after it.
func TestAMentionIsWorkedAsImg(t *testing.T) {
	h := newHarness(t)
	h.flow.Mention(context.Background(), command("@framytest_bot a fox"), " a fox ")
	h.flow.Wait()
	if job := h.store.jobs[1]; job == nil || job.n.Prompt != "a fox" {
		t.Fatalf("jobs %+v", h.store.jobs)
	}

	h = newHarness(t)
	h.flow.Mention(context.Background(), command("@framytest_bot"), "  ")
	h.flow.Wait()
	if sent := h.tg.methods("sendMessage"); len(sent) != 1 || sent[0].text != i18n.T("en", "guest.empty") || len(h.store.jobs) != 0 {
		t.Fatalf("sent %+v, jobs %+v", sent, h.store.jobs)
	}
}

// guestAnswered is the text of the one answer to a guest message.
func (h *harness) guestAnswered(t *testing.T) string {
	t.Helper()
	if len(h.tg.guestAnswers) != 1 {
		t.Fatalf("answers = %+v", h.tg.guestAnswers)
	}
	a, _ := h.tg.guestAnswers[0].(tg.InlineQueryResultArticle)
	text, _ := a.InputMessageContent.(tg.InputTextMessageContent)
	return text.MessageText
}

// A guest message through Guest is answered with its preview, the prompt
// trimmed; one without a query to answer is nothing to answer.
func TestAGuestMessageIsWorkedInTheBackground(t *testing.T) {
	h := guestHarness(t)
	h.flow.Guest(context.Background(), guestReply("gq-1"), "  make it snow ")
	h.flow.Wait()
	if text := h.guestAnswered(t); !strings.Contains(text, "make it snow") || h.store.stage(1) != db.StagePreview {
		t.Fatalf("answer %q, stage %s", text, h.store.stage(1))
	}

	h = guestHarness(t)
	h.flow.Guest(context.Background(), guestReply(""), "make it snow")
	h.flow.Wait()
	if len(h.tg.calls) != 0 || len(h.store.jobs) != 0 {
		t.Fatalf("calls %+v", h.tg.calls)
	}
}

// A guest message framy cannot work before its draft is told so, in the
// one answer it has: the photo it replied to cannot be read, or framy
// could not make room for it.
func TestAGuestMessageThatCannotBeWorkedIsToldWhy(t *testing.T) {
	h := guestHarness(t)
	h.files.fail = media.ErrUnreadable
	h.flow.Guest(context.Background(), guestReply("gq-1"), "make it snow")
	h.flow.Wait()
	if text := h.guestAnswered(t); text != i18n.T("en", "run.unsupported") || len(h.store.jobs) != 0 {
		t.Fatalf("answer %q, jobs %+v", text, h.store.jobs)
	}

	h = guestHarness(t)
	blocked := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.flow.cfg.Work = media.Work{Root: filepath.Join(blocked, "work")}
	h.flow.Guest(context.Background(), guestReply("gq-2"), "make it snow")
	h.flow.Wait()
	if text := h.guestAnswered(t); text != i18n.T("en", "run.failed") {
		t.Fatalf("answer %q", text)
	}
}

// A guest message replying to framy's answer while it has buttons is
// pointed at them, and makes no second run.
func TestAGuestReplyToALiveAnswerIsPointedAtItsButtons(t *testing.T) {
	h := guestHarness(t)
	h.flow.GuestOpen(context.Background(), guestReply("gq-1"))
	h.flow.Wait()
	if text := h.guestAnswered(t); text != i18n.T("en", "guest.open") || len(h.store.jobs) != 0 {
		t.Fatalf("answer %q, jobs %+v", text, h.store.jobs)
	}
	h.flow.GuestOpen(context.Background(), guestReply(""))
	h.flow.Wait()
	if len(h.tg.guestAnswers) != 1 {
		t.Fatalf("answers = %+v", h.tg.guestAnswers)
	}
}
