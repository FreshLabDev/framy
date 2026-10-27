// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"strconv"

	"github.com/FreshLabDev/tg"

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
	if cq.Message.Chat.ID != run.ChatID || cq.Message.MessageID != run.PreviewMessageID {
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
	switch action {
	case actGenerate:
		return f.generate(ctx, cq, run)
	case actNo:
		return f.no(ctx, cq, run)
	case actStop:
		return f.stop(ctx, cq, run)
	}
	return answer("")
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
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "run.limit", "limit", strconv.Itoa(limit.Limit)))
	case errors.As(err, &closed):
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "run.closed"))
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

// showLoop turns the preview into the generating loop.
func (f *Flow) showLoop(ctx context.Context, run db.ChatRun) {
	tk := f.ticker(run.JobID)
	tk.action, tk.caption = f.now(), f.now()
	loop, err := f.cfg.Renderer.Generating(ctx)
	if err == nil {
		_, err = f.edit(ctx, run.ChatID, run.PreviewMessageID, loop,
			generatingCaption(run.Lang, run.Prompt, run.Model, 0), generatingKeyboard(run.Lang, run.JobID))
	}
	if err != nil {
		// The run goes on; the caption still says so.
		f.log.Warn("generating loop not shown", "job", run.JobID, "error", err)
		_, _ = f.cfg.Telegram.EditMessageCaption(ctx, run.ChatID, run.PreviewMessageID,
			generatingCaption(run.Lang, run.Prompt, run.Model, 0), tg.EditCaptionOptions{Markup: generatingKeyboard(run.Lang, run.JobID)})
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

// stop cancels a queued job, or lets a running one end in the person's
// history without the chat, since a request already sent is paid for.
func (f *Flow) stop(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	if run.Stage != db.StageGenerating {
		return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	err := f.cfg.Queue.Cancel(ctx, run.JobID)
	switch {
	case errors.Is(err, jobs.ErrRunning):
		if _, err := f.cfg.Store.StopChatRun(ctx, run.JobID); err != nil {
			_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
			return err
		}
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, t(run.Lang, "run.stopped"))
		f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
		f.forget(run.JobID)
		return nil
	case err != nil:
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	f.close(ctx, run, db.StageGenerating)
	return nil
}

// close ends a run that leaves nothing in the chat: the preview or the loop
// is deleted.
func (f *Flow) close(ctx context.Context, run db.ChatRun, from string) {
	moved, err := f.cfg.Store.MoveChatRun(ctx, run.JobID, from, db.StageGone)
	if err != nil {
		f.log.Warn("run not closed", "job", run.JobID, "error", err)
		return
	}
	if !moved {
		return
	}
	f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
	f.forget(run.JobID)
}
