// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
)

// Again under a result, and Try again, Another model and Try without sound
// on the error card (docs/design.md, `/img` and `/edit` in a group): the
// press makes a new draft of the run, with its prompt, its files, its model
// and its settings, and a preview that replies to the message pressed. In a
// group the preview goes to the requester alone, as Animate's does. Who
// pays is asked again: a free run goes free while the tier has a slot and on
// the person's key past it, and a run on the key needs the key still there.
// Nothing runs until Generate, so the preview shows the price first.

// soundSetting is the setting that has a video model make its sound.
const soundSetting = "generate_audio"

// again makes a new draft of the run for action: Again on a done run, the
// error card's buttons on a failed one. Another model opens the list of
// models on the preview, and Try without sound turns the sound off.
func (f *Flow) again(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, action string) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	alert := func(key string, pairs ...string) error {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, key, pairs...))
	}
	from := db.StageFailed
	if action == actAgain {
		from = db.StageDone
	}
	if run.Stage != from || f.tapped(run.JobID, action) {
		return answer("")
	}
	s, err := f.stateOf(ctx, run)
	if err != nil {
		_ = answer("")
		return err
	}
	if len(s.inputs) != run.Taken {
		// A file of the run was deleted on the site since.
		return alert("result.gone")
	}
	d := s.draft
	d.id, d.note, d.album = 0, "", 0
	if action == actQuiet {
		if on, _ := d.settings[soundSetting].(bool); !on {
			return answer("")
		}
		d.settings = maps.Clone(d.settings)
		d.settings[soundSetting] = false
		d.estimate = price.Of(d.model, d.offer, requestOf(run.Prompt, d.settings, s.inputs))
	}
	if d.pay != payFree {
		has, err := f.hasKey(ctx, run.UserID, d.offer.Provider)
		if err != nil {
			_ = answer("")
			return err
		}
		if !has {
			return alert("run.need_key", "provider", providerName(d.offer.Provider))
		}
	}
	// The preview answers the message pressed as a command would be answered.
	pressed := run.ResultMessageID
	if from == db.StageFailed {
		pressed = run.PreviewMessageID
	}
	m := &tg.Message{
		MessageID: pressed, Chat: cq.Message.Chat, From: &cq.From,
		IsTopicMessage: run.ThreadID != 0, MessageThreadID: run.ThreadID,
	}
	ids := make([]int64, len(s.inputs))
	for i, a := range s.inputs {
		ids[i] = a.ID
	}
	// The press is the update: a replayed one makes no second draft.
	refusal, err := f.queueDraft(ctx, run.Lang, newJob(m, "tg:cb:"+cq.ID, run.Prompt, ids, &d), &d, true)
	switch {
	case errors.Is(err, jobs.ErrNoInput):
		return alert("result.gone")
	case err != nil:
		_ = answer("")
		return err
	case refusal != "":
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, refusal)
	case d.id == 0:
		return answer("")
	}
	f.madeBy(run.JobID, action)
	if d.pay == payFree {
		u, err := f.cfg.Allowance.Usage(ctx, run.UserID)
		if err != nil {
			f.log.Warn("free usage unread", "user", run.UserID, "error", err)
		}
		d.usage = u.Images
		if d.video() {
			d.usage = u.Videos
		}
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
	if d.paths, err = f.cardPictures(ctx, dir, s); err != nil {
		return fail(err)
	}
	open := foldAll
	if action == actOther {
		open = openModels
	}
	preview, ephemeral, err := f.sendPreview(ctx, m, d, run.Prompt, run.Lang, dir, cq.ID, open)
	if err != nil {
		return fail(err)
	}
	_ = answer("")
	r := db.ChatRun{
		JobID: d.id, ChatID: run.ChatID, ThreadID: run.ThreadID, CommandMessageID: pressed,
		PreviewMessageID: preview, PreviewEphemeral: ephemeral, Lang: run.Lang, Taken: run.Taken, UserID: run.UserID,
		ClipFileID: run.ClipFileID, ClipBytes: run.ClipBytes, ClipSeconds: run.ClipSeconds,
	}
	if err := f.cfg.Store.AddChatRun(ctx, r); err != nil {
		f.removePreview(ctx, r)
		f.cancel(ctx, d.id)
		return err
	}
	return nil
}

// original sends the image result as a file, as the model made it: a photo
// is what Telegram made of it, at most 2560 px on its long side and saved
// again as a JPEG, without a transparent background. It goes as a reply to
// the result, once, and the button goes from under the result.
func (f *Flow) original(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun) error {
	answer := func() error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "") }
	if run.Stage != db.StageDone {
		return answer()
	}
	results, err := f.cfg.Files.Results(ctx, run.JobID)
	if err != nil {
		_ = answer()
		return err
	}
	if len(results) == 0 || !strings.HasPrefix(results[0].ContentType, "image/") || !photoFits(results[0]) {
		return answer()
	}
	a := results[0]
	if _, sent := f.originals.LoadOrStore(run.JobID, struct{}{}); sent {
		return answer()
	}
	_ = answer()
	if err := f.cfg.Telegram.SendChatAction(ctx, run.ChatID, run.ThreadID, "upload_document"); err != nil {
		f.log.Debug("chat action not sent", "chat", run.ChatID, "error", err)
	}
	opts := tg.DocumentOptions{CaptionOptions: tg.CaptionOptions{ThreadID: run.ThreadID, ReplyTo: run.ResultMessageID}}
	if _, err := f.withResult(ctx, a, func(file tg.InputFile) (tg.Message, error) {
		return f.cfg.Telegram.SendDocument(ctx, run.ChatID, file, opts)
	}); err != nil {
		f.originals.Delete(run.JobID)
		return err
	}
	kb := resultKeyboard(run.Lang, run.JobID, a, fromChat(run), false)
	if err := f.cfg.Telegram.EditMessageReplyMarkup(ctx, run.ChatID, run.ResultMessageID, kb); err != nil && !tg.IsMessageNotModified(err) {
		f.log.Warn("Original not taken from the result", "job", run.JobID, "error", err)
	}
	return nil
}

// failureKeyboard is what helps after the run failed, as the site's card of
// a failed run has it (internal/web/app/src/errors.js): another try, another
// model, or the same run without its sound. A key out of money or refused
// needs the site, which the card's line says; past framy's own key a free
// run goes again on the person's, which the new preview shows. An edit of a
// clip keeps its model, and a run of the site has no card in the chat.
func failureKeyboard(run db.ChatRun) *tg.InlineKeyboardMarkup {
	if guest(run) || !fromChat(run) {
		return nil
	}
	retry := button(t(run.Lang, "web.act.retry"), callback(run.JobID, actRetry), "")
	other := button(t(run.Lang, "web.act.other_model"), callback(run.JobID, actOther), "")
	var row []tg.InlineKeyboardButton
	switch jobs.Class(run.ErrorClass) {
	case jobs.Money, jobs.Key:
		if run.Free {
			row = append(row, retry)
		}
	case jobs.Moderation, jobs.Refused, jobs.Faces, jobs.Invalid, jobs.NoAdapter:
		row = append(row, other)
	case jobs.Sound:
		if on, _ := run.Settings[soundSetting].(bool); on {
			row = append(row, button(t(run.Lang, "web.act.no_sound"), callback(run.JobID, actQuiet), ""))
		} else {
			row = append(row, retry)
		}
	default:
		row = append(row, retry, other)
	}
	if run.ClipFileID != "" {
		row = slices.DeleteFunc(row, func(b tg.InlineKeyboardButton) bool { return b.CallbackData == other.CallbackData })
	}
	if len(row) == 0 {
		return nil
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{row}}
}
