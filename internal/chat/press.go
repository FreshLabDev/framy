// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// press works a run's button. Only the requester presses; the press must
// come from the run's own message, since a client can send any callback data
// against any message it sees.
func (f *Flow) press(ctx context.Context, cq *tg.CallbackQuery) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	jobID, action, ok := parseCallback(cq.Data)
	if !ok {
		return answer("")
	}
	run, err := f.cfg.Store.ChatRun(ctx, jobID)
	if errors.Is(err, db.ErrNoRun) {
		return answer("")
	}
	if err != nil {
		_ = answer("")
		return err
	}
	if _, ok := pressedOn(cq, run, action); !ok {
		f.log.Debug("press on another message", "job", jobID)
		return answer("")
	}
	if cq.From.ID != run.UserID {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(f.personLang(ctx, cq.From), "run.not_yours"))
	}
	unlock := f.lock(jobID)
	defer unlock()
	// The delivery or another press may have moved the run meanwhile.
	if run, err = f.cfg.Store.ChatRun(ctx, jobID); err != nil {
		_ = answer("")
		return err
	}
	on, ok := pressedOn(cq, run, action)
	if !ok || !allowed(run, action) {
		return answer("")
	}
	switch {
	case action == actGenerate:
		return f.generate(ctx, cq, run)
	case action == actNo:
		return f.no(ctx, cq, run)
	case action == actStop:
		return f.stop(ctx, cq, run)
	case action == actModel:
		return f.view(ctx, cq, run, on, viewModel)
	case action == actSettings:
		return f.view(ctx, cq, run, on, viewSettings)
	case action == actBack:
		return f.view(ctx, cq, run, on, viewMain)
	case strings.HasPrefix(action, actFamily):
		return f.view(ctx, cq, run, on, viewFamily(strings.TrimPrefix(action, actFamily)))
	case action == actLocked:
		return f.view(ctx, cq, run, on, viewLocked)
	case strings.HasPrefix(action, actPickModel):
		return f.pickModel(ctx, cq, run, on, strings.TrimPrefix(action, actPickModel))
	case strings.HasPrefix(action, actPickSetting):
		return f.pickSetting(ctx, cq, run, on, strings.TrimPrefix(action, actPickSetting))
	case action == actVideo:
		return f.toVideo(ctx, cq, run, on)
	case action == actImage:
		return f.toImage(ctx, cq, run, on)
	case action == actKeyClip, action == actFreeClip:
		return f.otherLength(ctx, cq, run, on, action)
	case action == actAnimate:
		return f.animate(ctx, cq, run)
	}
	return answer("")
}

// allowed tells whether a run of its kind has the button, since old or
// forged data must change nothing. Generate, No and Stop every run has. A
// video run has no Model or Settings panel; only a video goes back to an
// image or takes another length of its clip, and only an image becomes a
// video or is animated.
func allowed(run db.ChatRun, action string) bool {
	video := catalog.Kind(run.Kind) == catalog.Video
	switch action {
	case actGenerate, actNo, actStop:
		return true
	case actImage, actKeyClip, actFreeClip:
		return video
	}
	return !video
}

// Where a press came from.
type place int

const (
	// onPreview: the message that shows the run, in the chat or, when it is
	// ephemeral, the requester's own.
	onPreview place = iota
	// onPanel: the requester's ephemeral panel over a preview in the chat.
	onPanel
	// onResult: the message the result went out in.
	onResult
)

// pressedOn tells which of the run's messages a press came from, if any:
// a client can send any callback data against any message it sees. Animate
// is on the result; the rest are on the preview or the panel. Ephemeral
// messages are numbered apart from the chat's.
func pressedOn(cq *tg.CallbackQuery, run db.ChatRun, action string) (place, bool) {
	if cq.Message == nil {
		// A guest run's answer is an inline message; it has no result
		// message of its own to animate.
		return onPreview, guest(run) && cq.InlineMessageID == run.InlineMessageID && action != actAnimate
	}
	if guest(run) || cq.Message.Chat.ID != run.ChatID {
		return 0, false
	}
	if action == actAnimate {
		return onResult, run.ResultMessageID != 0 && cq.Message.EphemeralMessageID == 0 && cq.Message.MessageID == run.ResultMessageID
	}
	if id := cq.Message.EphemeralMessageID; id != 0 {
		switch {
		case run.PreviewEphemeral && id == run.PreviewMessageID:
			return onPreview, true
		case run.PanelMessageID != 0 && id == run.PanelMessageID:
			return onPanel, true
		}
		return 0, false
	}
	return onPreview, !run.PreviewEphemeral && cq.Message.MessageID == run.PreviewMessageID
}

// generate queues the draft and turns the preview into the generating loop.
func (f *Flow) generate(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	if run.Stage != db.StagePreview {
		return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	err := f.cfg.Queue.Generate(ctx, run.JobID)
	var (
		limit  *free.LimitError
		closed *free.ClosedError
	)
	switch {
	case errors.Is(err, jobs.ErrNotDraft):
		// Ten minutes passed, or the job was cancelled: the preview goes.
		_ = f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "run.expired"))
		f.close(ctx, run, db.StagePreview)
		return nil
	case errors.As(err, &limit):
		// The free tier ran out between the preview and the press.
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, f.limitText(run.Lang, limit))
	case errors.As(err, &closed):
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, closedText(run.Lang, catalog.Kind(run.Kind)))
	case err != nil:
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	if _, err := f.cfg.Store.MoveChatRun(ctx, run.JobID, db.StagePreview, db.StageGenerating); err != nil {
		// The delivery moves a queued job's run itself.
		f.log.Warn("run not moved to generating", "job", run.JobID, "error", err)
	}
	f.showLoop(ctx, run)
	return nil
}

// showLoop turns the preview into the generating loop. The chat sees the
// loop: a preview only the requester saw gives way to a new message, as a
// reply to the command when the chat can see that.
func (f *Flow) showLoop(ctx context.Context, run db.ChatRun) {
	tk := f.ticker(run.JobID)
	tk.action, tk.caption = f.now(), f.now()
	if guest(run) {
		f.guestLoop(ctx, run)
		return
	}
	f.dropPanel(ctx, run)
	loop, err := f.cfg.Renderer.Generating(ctx)
	if run.PreviewEphemeral {
		f.deleteEphemeral(ctx, run.ChatID, run.UserID, run.PreviewMessageID)
		var sent tg.Message
		caption, kb := generatingCaption(run.Lang, run.Prompt, run.Model, 0), generatingKeyboard(run.Lang, run.JobID)
		if err == nil {
			sent, err = f.send(ctx, run.ChatID, run.CommandMessageID, run.ThreadID, loop, caption, kb)
		}
		if err != nil {
			f.log.Warn("generating loop not shown", "job", run.JobID, "error", err)
			sent, err = f.cfg.Telegram.SendReply(ctx, run.ChatID, run.CommandMessageID, run.ThreadID, caption, kb)
		}
		if err != nil {
			// The result still comes, as a new message.
			f.log.Warn("generating message not sent", "job", run.JobID, "error", err)
		}
		if err := f.cfg.Store.SetChatRunPreview(ctx, run.JobID, sent.MessageID, false); err != nil {
			f.log.Warn("generating message not recorded", "job", run.JobID, "error", err)
		}
		return
	}
	kb := generatingKeyboard(run.Lang, run.JobID)
	caption := generatingCaption(run.Lang, run.Prompt, run.Model, 0)
	if err == nil {
		_, err = f.edit(ctx, run.ChatID, run.PreviewMessageID, loop, caption, kb)
	}
	if err != nil {
		// The run goes on; the caption still says so.
		f.log.Warn("generating loop not shown", "job", run.JobID, "error", err)
		_, _ = f.cfg.Telegram.EditMessageCaption(ctx, run.ChatID, run.PreviewMessageID, caption, tg.EditCaptionOptions{Markup: kb})
	}
}

// no cancels the draft and deletes the preview.
func (f *Flow) no(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	if run.Stage != db.StagePreview {
		return nil
	}
	if err := f.cfg.Queue.Cancel(ctx, run.JobID); err != nil && !errors.Is(err, jobs.ErrRunning) {
		return err
	}
	f.close(ctx, run, db.StagePreview)
	return nil
}

// stop is Stop on the loop: see halt. A run that had started is told
// stopped in the press's answer.
func (f *Flow) stop(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	if run.Stage != db.StageGenerating {
		return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	running, err := f.halt(ctx, run)
	text := ""
	if running {
		text = t(run.Lang, "run.stopped")
	}
	_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text)
	return err
}

// halt cancels a queued job, or lets a running one end in the person's
// history without the chat, since a request already sent is paid for. The
// loop goes either way. It tells whether the job was running.
func (f *Flow) halt(ctx context.Context, run db.ChatRun) (running bool, err error) {
	err = f.cfg.Queue.Cancel(ctx, run.JobID)
	switch {
	case errors.Is(err, jobs.ErrRunning):
		if _, err := f.cfg.Store.StopChatRun(ctx, run.JobID); err != nil {
			return false, err
		}
		f.removePreview(ctx, run)
		f.forget(run.JobID)
		return true, nil
	case err != nil:
		return false, err
	}
	f.close(ctx, run, db.StageGenerating)
	return false, nil
}

// close ends a run that leaves nothing in the chat: the preview or the loop
// is deleted, and the panel with them.
func (f *Flow) close(ctx context.Context, run db.ChatRun, from string) {
	moved, err := f.cfg.Store.MoveChatRun(ctx, run.JobID, from, db.StageGone)
	if err != nil {
		f.log.Warn("run not closed", "job", run.JobID, "error", err)
		return
	}
	if !moved {
		return
	}
	run.Stage = from
	f.removePreview(ctx, run)
	f.forget(run.JobID)
}
