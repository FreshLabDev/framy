// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// The cadence of a running job in its chat (docs/design.md, Step by step).
const (
	// followEvery is how often the open runs are read.
	followEvery = 2 * time.Second
	// actionEvery repeats the chat action, which Telegram shows for 5 s.
	actionEvery = 4500 * time.Millisecond
	// captionEvery is how often the caption tells the time taken; a group
	// takes about 20 bot messages a minute.
	captionEvery = 10 * time.Second
	// inPlaceWithin: an image ready this soon after Generate replaces the
	// loop; a later one comes as a reply, which notifies, and the loop goes.
	inPlaceWithin = 25 * time.Second
	// following is how many runs are worked at once.
	following = 4
)

// Telegram's limits on a photo; a bigger image goes as a document.
const (
	maxPhotoBytes = 10 << 20
	maxPhotoSides = 10000 // width + height
	maxPhotoRatio = 20
)

// ticker is when a running job last told its chat something.
type ticker struct {
	mu              sync.Mutex
	action, caption time.Time
}

func (f *Flow) ticker(jobID int64) *ticker {
	v, _ := f.ticks.LoadOrStore(jobID, &ticker{})
	return v.(*ticker)
}

// Run follows the open runs until ctx ends: the progress of a running job,
// and its result or error once it ends. It draws the generating loop first,
// so the first Generate does not wait for it.
func (f *Flow) Run(ctx context.Context) {
	if _, err := f.cfg.Renderer.Generating(ctx); err != nil && ctx.Err() == nil {
		f.log.Warn("generating loop not drawn ahead", "error", err)
	}
	tick := time.NewTicker(followEvery)
	defer tick.Stop()
	for {
		f.follow(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// follow works every open run once.
func (f *Flow) follow(ctx context.Context) {
	runs, err := f.cfg.Store.OpenChatRuns(ctx)
	if err != nil {
		if ctx.Err() == nil {
			f.log.Error("open chat runs unread", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	slot := make(chan struct{}, following)
	for _, r := range runs {
		wg.Go(func() {
			slot <- struct{}{}
			defer func() { <-slot }()
			f.step(ctx, r.JobID)
		})
	}
	wg.Wait()
}

// step moves one run on. A press working on the run has it; the next round
// looks again.
func (f *Flow) step(ctx context.Context, jobID int64) {
	v, _ := f.locks.LoadOrStore(jobID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return
	}
	defer mu.Unlock()
	run, err := f.cfg.Store.ChatRun(ctx, jobID)
	if err != nil {
		f.log.Warn("chat run unread", "job", jobID, "error", err)
		return
	}
	switch run.Stage {
	case db.StagePreview:
		switch jobs.Status(run.Status) {
		case jobs.Draft:
		case jobs.Cancelled:
			// Ten minutes passed without a press, or the site cancelled it.
			f.close(ctx, run, db.StagePreview)
		default:
			// Generate queued the job, and the run did not move with it.
			if _, err := f.cfg.Store.MoveChatRun(ctx, jobID, db.StagePreview, db.StageGenerating); err != nil {
				f.log.Warn("run not moved to generating", "job", jobID, "error", err)
				return
			}
			run.Stage, run.GenerateAt = db.StageGenerating, f.now()
			f.showLoop(ctx, run)
		}
	case db.StageGenerating:
		switch jobs.Status(run.Status) {
		case jobs.Queued, jobs.Running:
			f.progress(ctx, run)
		case jobs.Done:
			f.deliver(ctx, run)
		case jobs.Failed, jobs.Expired:
			f.failed(ctx, run, reason(run))
		case jobs.Cancelled:
			f.close(ctx, run, db.StageGenerating)
		}
	}
}

// progress keeps the chat action going and the time on the caption.
func (f *Flow) progress(ctx context.Context, run db.ChatRun) {
	tk := f.ticker(run.JobID)
	tk.mu.Lock()
	defer tk.mu.Unlock()
	now := f.now()
	if now.Sub(tk.action) >= actionEvery {
		tk.action = now
		if err := f.cfg.Telegram.SendChatAction(ctx, run.ChatID, run.ThreadID, "upload_photo"); err != nil {
			f.log.Debug("chat action not sent", "chat", run.ChatID, "error", err)
		}
	}
	if now.Sub(tk.caption) >= captionEvery && !run.GenerateAt.IsZero() {
		tk.caption = now
		caption := generatingCaption(run.Lang, run.Prompt, run.Model, now.Sub(run.GenerateAt))
		_, err := f.cfg.Telegram.EditMessageCaption(ctx, run.ChatID, run.PreviewMessageID, caption,
			tg.EditCaptionOptions{Markup: generatingKeyboard(run.Lang, run.JobID)})
		if err != nil && !tg.IsMessageNotModified(err) {
			f.log.Debug("caption not updated", "job", run.JobID, "error", err)
		}
	}
}

// deliver puts the result in the chat: in place of the loop when it came
// quickly, as a reply to the command otherwise.
func (f *Flow) deliver(ctx context.Context, run db.ChatRun) {
	results, err := f.cfg.Files.Results(ctx, run.JobID)
	if err != nil {
		f.log.Warn("results unread", "job", run.JobID, "error", err)
		return
	}
	if len(results) == 0 {
		f.failed(ctx, run, "err.failed")
		return
	}
	a := results[0]
	caption := resultCaption(run.Prompt, run.Model)
	asPhoto := photoFits(a)
	var msg tg.Message
	sent := false
	if asPhoto && f.now().Sub(run.GenerateAt) <= inPlaceWithin {
		msg, err = f.withResult(ctx, a, func(file tg.InputFile) (tg.Message, error) {
			return f.cfg.Telegram.EditMessageMediaMessage(ctx, run.ChatID, run.PreviewMessageID, tg.InputMediaPhoto{Media: file, Caption: caption}, nil)
		})
		if err == nil {
			sent = true
		} else {
			f.log.Warn("result not put in place; replying", "job", run.JobID, "error", err)
		}
	}
	if !sent {
		opts := tg.CaptionOptions{ThreadID: run.ThreadID, ReplyTo: run.CommandMessageID, Caption: caption}
		msg, err = f.withResult(ctx, a, func(file tg.InputFile) (tg.Message, error) {
			if asPhoto {
				return f.cfg.Telegram.SendPhoto(ctx, run.ChatID, file, opts)
			}
			return f.cfg.Telegram.SendDocument(ctx, run.ChatID, file, tg.DocumentOptions{CaptionOptions: opts})
		})
		if err != nil {
			f.log.Error("result not sent", "job", run.JobID, "error", err)
			return
		}
		f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
	}
	res := db.ChatResult{MessageID: msg.MessageID, FileID: fileIDOf(msg), FileUniqueID: fileUniqueIDOf(msg)}
	if _, err := f.cfg.Store.FinishChatRun(ctx, run.JobID, db.StageDone, res); err != nil {
		f.log.Error("run not finished", "job", run.JobID, "error", err)
		return
	}
	f.react(ctx, run.ChatID, run.CommandMessageID, reactDone)
	f.forget(run.JobID)
}

// withResult reads the result from the bucket and hands it to send.
func (f *Flow) withResult(ctx context.Context, a storage.Asset, send func(tg.InputFile) (tg.Message, error)) (tg.Message, error) {
	body, err := f.cfg.Files.Open(ctx, a)
	if err != nil {
		return tg.Message{}, err
	}
	defer body.Close()
	return send(tg.InputFileUpload{Filename: "framy" + extOf(a.ContentType), Data: body})
}

func extOf(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	}
	return ""
}

// photoFits tells whether Telegram takes the file as a photo.
func photoFits(a storage.Asset) bool {
	if !strings.HasPrefix(a.ContentType, "image/") || a.Bytes > maxPhotoBytes {
		return false
	}
	if a.Width <= 0 || a.Height <= 0 {
		return true
	}
	long, short := max(a.Width, a.Height), min(a.Width, a.Height)
	return a.Width+a.Height <= maxPhotoSides && long <= short*maxPhotoRatio
}

// reason is the i18n key of the line a failed run's card says.
func reason(run db.ChatRun) string {
	if jobs.Status(run.Status) == jobs.Expired {
		return "err.timeout"
	}
	switch jobs.Class(run.ErrorClass) {
	case jobs.Moderation:
		return "err.moderation"
	case jobs.Money:
		if run.Free {
			return "err.failed"
		}
		return "err.money"
	case jobs.Key:
		return "err.key"
	case jobs.Timeout:
		return "err.timeout"
	}
	return "err.failed"
}

// failed turns the loop into the error card. Media cannot turn back into
// text, so the card is a picture; where its faces lack the letters, the
// line goes in the caption.
func (f *Flow) failed(ctx context.Context, run db.ChatRun, key string) {
	text := t(run.Lang, key)
	card, err := f.cfg.Renderer.ErrorCard(ctx, text, run.Lang)
	if err == nil {
		line := ""
		if !card.TextDrawn {
			line = text
		}
		caption := lines(promptQuote(run.Prompt), line, "<b>"+html.EscapeString(run.Model)+"</b>")
		_, err = f.edit(ctx, run.ChatID, run.PreviewMessageID, card, caption, nil)
	}
	if err != nil {
		f.log.Warn("error card not shown; replying", "job", run.JobID, "error", err)
		if _, rerr := f.cfg.Telegram.SendReply(ctx, run.ChatID, run.CommandMessageID, run.ThreadID, text, nil); rerr != nil {
			f.log.Error("failure not told", "job", run.JobID, "error", rerr)
			return
		}
		f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
	}
	if _, err := f.cfg.Store.FinishChatRun(ctx, run.JobID, db.StageFailed, db.ChatResult{}); err != nil {
		f.log.Error("run not finished", "job", run.JobID, "error", err)
		return
	}
	f.react(ctx, run.ChatID, run.CommandMessageID, "")
	f.forget(run.JobID)
}
