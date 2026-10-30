// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/price"
)

// The other length of a clip (docs/design.md, `/edit`). The length is the
// person's choice (owner, 27 October): the free video takes the first 5 s
// of a longer clip, and its preview offers as much as the model takes on the
// person's key; that preview offers the free 5 s back while the week's
// video is left. Only the part a run takes is kept, so the clip is read from
// Telegram again and kept at the other length, the draft takes it in place
// of the one it had, and the card is drawn again.

// clipChoice is the other length of the clip a preview offers.
type clipChoice struct {
	// action is actKeyClip or actFreeClip.
	action string
	// seconds is how much of the clip the run takes; whole, all of it.
	seconds float64
	whole   bool
}

// otherLength is the other length of the clip the preview offers, if
// there is one: on the key while the run takes less of the clip than the
// key would, which a free run always does, and the free 5 s back while the
// week's video is left.
func (d draft) otherLength() (clipChoice, bool) {
	freeMax := free.VideoEdit.MaxSourceSeconds
	if !d.clip || d.clipSeconds <= freeMax+clipSlack {
		// The free video takes the clip whole.
		return clipChoice{}, false
	}
	keyMax := 0.0
	if s := d.model.Traits.SourceVideo; s != nil {
		keyMax = float64(s.MaxSeconds)
	}
	onKey := clipChoice{action: actKeyClip, seconds: keyMax}
	if d.clipSeconds <= keyMax+clipSlack {
		onKey = clipChoice{action: actKeyClip, seconds: d.clipSeconds, whole: true}
	}
	switch {
	case keyMax <= freeMax:
		return clipChoice{}, false
	case d.pay == payFree, d.keptSeconds < onKey.seconds-clipSlack:
		// A key run on a part as short as the free one comes from the queue
		// refusing the free video after the clip was kept.
		return onKey, true
	case d.freeLeft:
		return clipChoice{action: actFreeClip, seconds: freeMax}, true
	}
	return clipChoice{}, false
}

// label is the choice's button. One on a key the person does not have is
// marked 🔑, as such a model is in the list, and its press says where to
// connect one.
func (c clipChoice) label(lang string, d draft) string {
	n := wholeSeconds(c.seconds)
	if c.action == actFreeClip {
		return t(lang, "edit.free_first", "n", n)
	}
	text := t(lang, "edit.key_first", "n", n)
	if c.whole {
		text = t(lang, "edit.key_whole", "n", n)
	}
	if !d.hasKey {
		text = "🔑 " + text
	}
	return text
}

// cut is the part of the clip the choice keeps: nil for the whole clip.
func (c clipChoice) cut() *media.Cut {
	if c.whole {
		return nil
	}
	return &media.Cut{Seconds: c.seconds}
}

// otherLength takes the length of the run's clip that the preview offers.
// The press is answered at once, with a word that the clip is being taken,
// since reading and keeping it can take seconds; whatever goes wrong after
// that leaves the draft as the card shows it.
func (f *Flow) otherLength(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place, action string) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StagePreview || run.ClipFileID == "" {
		return answer("")
	}
	s, err := f.stateOf(ctx, run)
	if err != nil {
		_ = answer("")
		return err
	}
	c, ok := s.otherLength()
	if !ok || c.action != action {
		// The card changed since the button was drawn.
		return answer("")
	}
	onFree := action == actFreeClip
	if !onFree && !s.hasKey {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "panel.key_needed"))
	}
	_ = answer(t(run.Lang, "edit.taking", "n", wholeSeconds(c.seconds)))
	if err := f.cfg.Telegram.SendChatAction(ctx, run.ChatID, run.ThreadID, chatAction(true)); err != nil {
		f.log.Debug("chat action not sent", "chat", run.ChatID, "error", err)
	}
	dir, done, err := f.cfg.Work.Dir("chat")
	if err != nil {
		return err
	}
	defer done()
	src := filepath.Join(dir, media.In)
	if err := f.download(ctx, run.ClipFileID, run.ClipBytes, f.cfg.MaxVideo, src); err != nil {
		return fmt.Errorf("read the clip again: %w", err)
	}
	a, err := f.cfg.Files.KeepVideo(ctx, run.UserID, src, c.cut())
	if err != nil {
		return err
	}
	if limits := s.model.Traits.SourceVideo; limits != nil && limits.MaxMiB > 0 && a.Bytes > int64(limits.MaxMiB)<<20 {
		return fmt.Errorf("%g s of the clip: %w", c.seconds, errTooBig)
	}
	est := price.Of(s.model, s.offer, priceRequest(run.Prompt, s.settings, nil, &price.Video{Seconds: a.Seconds, Width: a.Width, Height: a.Height}))
	n := jobs.New{
		UserID: run.UserID, Offer: s.offer, Prompt: run.Prompt, Settings: s.settings,
		Free: onFree, Estimate: &est, Inputs: []int64{a.ID},
	}
	if q := s.quote; q.Code != fx.USD {
		n.FX = &q
	}
	err = f.cfg.Queue.Revise(ctx, run.JobID, n)
	var (
		limit  *free.LimitError
		closed *free.ClosedError
	)
	switch {
	case errors.Is(err, jobs.ErrNotDraft):
		f.close(ctx, run, db.StagePreview)
		return nil
	case errors.As(err, &limit), errors.As(err, &closed):
		// The free video went between the card and the press: the draft
		// stays on the key, as the card shows it.
		f.log.Info("the free length refused; the run stays on the key", "job", run.JobID, "error", err)
		return nil
	case err != nil:
		return err
	}
	note := ""
	if c.cut() != nil {
		note = noteClipCut
	}
	if err := f.cfg.Store.SetChatRunNote(ctx, run.JobID, note); err != nil {
		return err
	}
	fresh, err := f.cfg.Store.ChatRun(ctx, run.JobID)
	if err != nil {
		return err
	}
	if s, err = f.stateOf(ctx, fresh); err != nil {
		return err
	}
	// The part kept starts where the clip does, so its first frame is the
	// clip's.
	poster, err := f.cfg.Clips.Poster(ctx, dir)
	if err != nil {
		return err
	}
	return f.drawCard(ctx, fresh, on, viewMain, s, dir, []string{poster})
}
