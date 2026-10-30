// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"fmt"
	"html"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
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
// so the first Generate does not wait for it. Beside them it sends the
// results made on the site to the DMs.
func (f *Flow) Run(ctx context.Context) {
	var site sync.WaitGroup
	site.Go(func() { f.runSite(ctx) })
	defer site.Wait()
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
	if guest(run) {
		// framy is not in the chat to show an action there.
		if now.Sub(tk.caption) >= captionEvery && !run.GenerateAt.IsZero() {
			tk.caption = now
			f.guestCaption(ctx, run, generatingCaption(run.Lang, run.Prompt, run.Model, now.Sub(run.GenerateAt)), generatingKeyboard(run.Lang, run.JobID))
		}
		return
	}
	if now.Sub(tk.action) >= actionEvery {
		tk.action = now
		if err := f.cfg.Telegram.SendChatAction(ctx, run.ChatID, run.ThreadID, chatAction(catalog.Kind(run.Kind) == catalog.Video)); err != nil {
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
	if guest(run) {
		f.guestDeliver(ctx, run, a)
		return
	}
	caption := resultCaption(run.Prompt, run.Model)
	var msg tg.Message
	if strings.HasPrefix(a.ContentType, "video/") {
		msg, err = f.sendVideo(ctx, run, a, caption)
	} else {
		msg, err = f.sendImage(ctx, run, a, caption)
	}
	if err != nil {
		f.log.Error("result not sent", "job", run.JobID, "error", err)
		return
	}
	res := resultOf(msg)
	if _, err := f.cfg.Store.FinishChatRun(ctx, run.JobID, db.StageDone, res); err != nil {
		f.log.Error("run not finished", "job", run.JobID, "error", err)
		return
	}
	f.forget(run.JobID)
}

// sendImage puts a picture in the chat: a photo, or a document when
// Telegram would not take it as one, with Animate under it. The loop goes
// once a reply is sent.
func (f *Flow) sendImage(ctx context.Context, run db.ChatRun, a storage.Asset, caption string) (tg.Message, error) {
	asPhoto := photoFits(a)
	kb := resultKeyboard(run.Lang, run.JobID)
	if asPhoto && f.now().Sub(run.GenerateAt) <= inPlaceWithin {
		msg, err := f.withResult(ctx, a, func(file tg.InputFile) (tg.Message, error) {
			return f.cfg.Telegram.EditMessageMediaMessage(ctx, run.ChatID, run.PreviewMessageID, tg.InputMediaPhoto{Media: file, Caption: caption}, kb)
		})
		if err == nil {
			return msg, nil
		}
		f.log.Warn("result not put in place; replying", "job", run.JobID, "error", err)
	}
	opts := tg.CaptionOptions{ThreadID: run.ThreadID, ReplyTo: run.CommandMessageID, Caption: caption, Markup: kb}
	msg, err := f.postImage(ctx, run.ChatID, a, opts)
	if err != nil {
		return tg.Message{}, err
	}
	f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
	return msg, nil
}

// postImage sends a picture to the chat as a new message: a photo, or a
// document when Telegram would not take it as one.
func (f *Flow) postImage(ctx context.Context, chatID int64, a storage.Asset, opts tg.CaptionOptions) (tg.Message, error) {
	return f.withResult(ctx, a, func(file tg.InputFile) (tg.Message, error) {
		if photoFits(a) {
			return f.cfg.Telegram.SendPhoto(ctx, chatID, file, opts)
		}
		return f.cfg.Telegram.SendDocument(ctx, chatID, file, tg.DocumentOptions{CaptionOptions: opts})
	})
}

// sendVideo puts a video in the chat as one Telegram plays while it loads:
// always as a reply to the command, which notifies, since a video takes
// longer than an in-place result is waited for, and the loop goes. The
// result goes through the clip pipeline first, which moves the index to the
// front and copies the streams when they are fit; a result the pipeline
// cannot read goes as it came. Its cover is the frame the result card would
// show, so every client opens on the same picture.
func (f *Flow) sendVideo(ctx context.Context, run db.ChatRun, a storage.Asset, caption string) (tg.Message, error) {
	opts := tg.CaptionOptions{ThreadID: run.ThreadID, ReplyTo: run.CommandMessageID, Caption: caption}
	msg, err := f.postVideo(ctx, run.ChatID, a, opts)
	if err != nil {
		return tg.Message{}, err
	}
	f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
	return msg, nil
}

// postVideo readies a video result and sends it to the chat as a new
// message, with its cover.
func (f *Flow) postVideo(ctx context.Context, chatID int64, a storage.Asset, co tg.CaptionOptions) (tg.Message, error) {
	dir, done, err := f.cfg.Work.Dir("result")
	if err != nil {
		return tg.Message{}, err
	}
	defer done()
	v, streams, err := f.readyVideo(ctx, a, dir)
	if err != nil {
		return tg.Message{}, err
	}
	opts := tg.VideoOptions{
		CaptionOptions: co,
		Width:          v.Width, Height: v.Height, Duration: int(math.Ceil(v.Seconds)), SupportsStreaming: streams,
	}
	return f.uploadVideo(ctx, chatID, v.Path, f.coverOf(ctx, dir, a.ID), opts)
}

// uploadVideo sends the video at path, with the cover at cover when there is
// one. A cover Telegram refuses does not keep the video back: it goes again
// without one, and clients pick a frame of their own.
func (f *Flow) uploadVideo(ctx context.Context, chatID int64, path, cover string, opts tg.VideoOptions) (tg.Message, error) {
	send := func(o tg.VideoOptions) (tg.Message, error) {
		return withFileAt(path, "framy.mp4", func(file tg.InputFile) (tg.Message, error) {
			return f.cfg.Telegram.SendVideo(ctx, chatID, file, o)
		})
	}
	if cover == "" {
		return send(opts)
	}
	msg, err := withFileAt(cover, "cover.jpg", func(c tg.InputFile) (tg.Message, error) {
		o := opts
		o.Cover = c
		return send(o)
	})
	var apiErr *tg.APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.ErrorCode != 400 {
		return msg, err
	}
	f.log.Warn("cover refused; video sent without one", "error", err)
	return send(opts)
}

// coverQuality is the JPEG quality of a cover: no visible loss, and a
// frame stays far below a photo's 10 MB, the limit Telegram puts on a cover.
const coverQuality = 90

// coverOf writes the cover of the video read into dir and returns its path:
// the frame the result card shows, the first that is not one colour, as a
// JPEG, which is a small upload next to the video. It returns "" when there
// is none, and the video goes without.
func (f *Flow) coverOf(ctx context.Context, dir string, asset int64) string {
	poster, err := f.cfg.Clips.Poster(ctx, dir)
	if err == nil {
		var path string
		if path, err = jpegCover(poster, filepath.Join(dir, "cover.jpg")); err == nil {
			return path
		}
	}
	if ctx.Err() == nil {
		f.log.Warn("video result has no cover", "asset", asset, "error", err)
	}
	return ""
}

// jpegCover writes the PNG at in as the JPEG at out, when Telegram takes it
// as a photo.
func jpegCover(in, out string) (string, error) {
	src, err := os.Open(in)
	if err != nil {
		return "", err
	}
	img, err := png.Decode(src)
	src.Close()
	if err != nil {
		return "", fmt.Errorf("read the poster: %w", err)
	}
	b := img.Bounds()
	if !photoFits(storage.Asset{ContentType: "image/jpeg", Width: b.Dx(), Height: b.Dy()}) {
		return "", fmt.Errorf("a %dx%d frame is no photo", b.Dx(), b.Dy())
	}
	dst, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	err = jpeg.Encode(dst, img, &jpeg.Options{Quality: coverQuality})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", err
	}
	if st.Size() > maxPhotoBytes {
		return "", fmt.Errorf("a cover of %d bytes is no photo", st.Size())
	}
	return out, nil
}

// readyVideo reads a video result into dir and makes it what Telegram
// streams: streams tells it did. When the pipeline fails, the file goes as
// it came, with what framy read of it when it was kept, and is not said to
// stream, since its index may be at the end.
func (f *Flow) readyVideo(ctx context.Context, a storage.Asset, dir string) (v media.Video, streams bool, err error) {
	in := filepath.Join(dir, media.In)
	body, err := f.cfg.Files.Open(ctx, a)
	if err != nil {
		return media.Video{}, false, err
	}
	out, err := os.OpenFile(in, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		body.Close()
		return media.Video{}, false, err
	}
	_, err = io.Copy(out, body)
	body.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return media.Video{}, false, fmt.Errorf("read the result: %w", err)
	}
	if v, err = f.cfg.Clips.NormalizeVideo(ctx, dir, nil); err == nil {
		return v, true, nil
	}
	if ctx.Err() != nil {
		return media.Video{}, false, err
	}
	f.log.Warn("video result sent as it came", "asset", a.ID, "error", err)
	return media.Video{Path: in, ContentType: a.ContentType, Width: a.Width, Height: a.Height, Seconds: a.Seconds}, false, nil
}

// withFileAt hands send the file at path as an upload named name.
func withFileAt(path, name string, send func(tg.InputFile) (tg.Message, error)) (tg.Message, error) {
	file, err := os.Open(path)
	if err != nil {
		return tg.Message{}, err
	}
	defer file.Close()
	return send(tg.InputFileUpload{Filename: name, Data: file})
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
	case jobs.Faces:
		return "err.faces"
	case jobs.Money:
		if run.Free {
			return "err.failed"
		}
		return "err.money"
	case jobs.Key:
		if run.Free {
			// framy's own key, not the person's.
			return "err.failed"
		}
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
	if guest(run) {
		f.guestFailed(ctx, run, key)
		return
	}
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
	f.forget(run.JobID)
}
