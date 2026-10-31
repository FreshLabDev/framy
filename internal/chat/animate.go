// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"path/filepath"
	"strconv"
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

// tapAgain is how soon another press of the same button that makes a
// draft makes nothing: a double tap is one press.
const tapAgain = 5 * time.Second

// tapped tells whether the button made a draft a moment ago.
func (f *Flow) tapped(jobID int64, action string) bool {
	at, ok := f.made.Load(strconv.FormatInt(jobID, 10) + ":" + action)
	return ok && f.now().Sub(at.(time.Time)) < tapAgain
}

// madeBy notes that the button made a draft.
func (f *Flow) madeBy(jobID int64, action string) {
	f.made.Store(strconv.FormatInt(jobID, 10)+":"+action, f.now())
}

// resultKeyboard are the buttons under a result a: Animate under an image,
// Again under a run made in the chat (again), and Original under an image
// that went as a photo while its file has not gone out yet (original). A
// result with none has no keyboard.
func resultKeyboard(lang string, jobID int64, a storage.Asset, again, original bool) *tg.InlineKeyboardMarkup {
	image := strings.HasPrefix(a.ContentType, "image/")
	var top, more []tg.InlineKeyboardButton
	if image {
		top = append(top, button(t(lang, "result.animate"), callback(jobID, actAnimate), ""))
	}
	if again {
		top = append(top, button(t(lang, "web.act.again"), callback(jobID, actAgain), ""))
	}
	if image && original && photoFits(a) {
		more = append(more, button(t(lang, "result.original"), callback(jobID, actOriginal), ""))
	}
	var rows [][]tg.InlineKeyboardButton
	for _, row := range [][]tg.InlineKeyboardButton{top, more} {
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// animate makes the run's image result the start frame of a new video run.
func (f *Flow) animate(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StageDone {
		return answer("")
	}
	if f.tapped(run.JobID, actAnimate) {
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
	d := draft{model: model, offer: offer, pay: payFree, settings: settings, taken: 1, images: []price.Image{{Width: frame.Width, Height: frame.Height}}}
	d.estimate = price.Of(model, offer, requestOf(run.Prompt, settings, []storage.Asset{frame}))
	if d.quote, err = f.quote(ctx, run.UserID, run.Lang); err != nil {
		_ = answer("")
		return err
	}
	// The press is the update: a replayed one makes no second draft.
	key := "tg:cb:" + cq.ID
	refusal, err := f.queueDraft(ctx, run.Lang, newJob(m, key, run.Prompt, []int64{frame.ID}, &d), &d, true)
	switch {
	case err != nil:
		_ = answer("")
		return err
	case refusal != "":
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, refusal)
	case d.id == 0:
		return answer("")
	}
	f.madeBy(run.JobID, actAnimate)
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
	preview, ephemeral, err := f.sendPreview(ctx, m, d, run.Prompt, run.Lang, dir, cq.ID, foldAll)
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
