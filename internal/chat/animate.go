// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
)

// Animate on an image result (docs/design.md, Actions on a result): the
// image becomes the start frame of a video. The press makes a new draft with
// the image's prompt, on the free video or past it on the person's key, and
// its preview replies to the result. In a group the preview goes to the
// requester alone, which the press lets framy do within 15 seconds, admin
// or not.

// animateAgain is how soon another Animate on the same result makes
// nothing: a double tap is one press.
const animateAgain = 5 * time.Second

// resultKeyboard is the button under an image result.
func resultKeyboard(lang string, jobID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		button(t(lang, "result.animate"), callback(jobID, actAnimate), ""),
	}}}
}

// animate makes the run's image result the start frame of a new video run.
func (f *Flow) animate(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StageDone {
		return answer("")
	}
	if at, ok := f.animated.Load(run.JobID); ok && f.now().Sub(at.(time.Time)) < animateAgain {
		return answer("")
	}
	results, err := f.cfg.Files.Results(ctx, run.JobID)
	if err != nil {
		_ = answer("")
		return err
	}
	if len(results) == 0 || !strings.HasPrefix(results[0].ContentType, "image/") {
		return answer("")
	}
	frame := results[0]
	plan, model, offer, err := f.videoPlan(ctx)
	if err != nil {
		_ = answer("")
		return err
	}
	// The preview answers the result as a command would be answered.
	m := &tg.Message{
		MessageID: run.ResultMessageID, Chat: cq.Message.Chat, From: &cq.From,
		IsTopicMessage: run.ThreadID != 0, MessageThreadID: run.ThreadID,
	}
	settings := plan.SettingsFor(1)
	d := draft{model: model, offer: offer, pay: payFree, settings: settings, taken: 1}
	d.estimate = price.Of(model, offer, requestOf(run.Prompt, settings, []storage.Asset{frame}))
	if d.quote, err = f.quote(ctx, run.UserID, run.Lang); err != nil {
		_ = answer("")
		return err
	}
	// The press is the update: a replayed one makes no second draft.
	key := "tg:cb:" + cq.ID
	refusal, err := f.queueDraft(ctx, run.Lang, newJob(m, key, run.Prompt, []int64{frame.ID}, &d), &d)
	switch {
	case err != nil:
		_ = answer("")
		return err
	case refusal != "":
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, refusal)
	case d.id == 0:
		return answer("")
	}
	f.animated.Store(run.JobID, f.now())
	if d.pay == payFree {
		u, err := f.cfg.Allowance.Usage(ctx, run.UserID)
		if err != nil {
			f.log.Warn("free usage unread", "user", run.UserID, "error", err)
		}
		d.usage = u.Videos
	}
	fail := func(err error) error {
		_ = answer("")
		f.cancel(ctx, d.id)
		return err
	}
	dir, done, err := f.cfg.Work.Dir("chat")
	if err != nil {
		return fail(err)
	}
	defer done()
	path := filepath.Join(dir, "in-0")
	if err := f.save(ctx, frame, path); err != nil {
		return fail(err)
	}
	d.paths = []string{path}
	preview, ephemeral, err := f.sendPreview(ctx, m, d, run.Prompt, run.Lang, dir, cq.ID)
	if err != nil {
		return fail(err)
	}
	_ = answer("")
	r := db.ChatRun{
		JobID: d.id, ChatID: run.ChatID, ThreadID: run.ThreadID, CommandMessageID: run.ResultMessageID,
		PreviewMessageID: preview, PreviewEphemeral: ephemeral, Lang: run.Lang, Taken: 1, UserID: run.UserID,
	}
	if err := f.cfg.Store.AddChatRun(ctx, r); err != nil {
		f.removePreview(ctx, r)
		f.cancel(ctx, d.id)
		return err
	}
	return nil
}
