// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"fmt"
	"html"
	"image"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/storage"
)

// Guest mode (docs/design.md, Guest mode): `@framyin_bot …` in a chat framy
// is not a member of, in reply to a photo or not. Telegram sends the bot that
// message and the one it replied to, and framy answers it once. The answer is
// an inline message, so the run is the same as in a group with every message
// replaced by that one: the preview, the loop and the result or the error
// card are edits of it by inline_message_id. Telegram takes no upload in an
// inline message, so each picture goes by the file_id Telegram has of it or
// by a link of framy's it fetches (share.go), and nothing can be deleted.
// The preview is text with the card as its link preview: text can become a
// picture and back to one line, a picture never becomes text. So No leaves
// one line, and Stop, on the loop, leaves the card it started from.

// The parts a link of a guest run serves, beside a result's.
const (
	shareCard    byte = 'r' // the preview card as the run is now
	shareLoop    byte = 'l' // the generating loop
	shareFailure byte = 'e' // the error card of a failed run
)

// guestCardSide is the long side of the card as a link serves it, the size
// anim draws it at.
const guestCardSide = 1280

// guestKey is the dedupe key of a guest message: its query is answered
// once, and a replayed update carries the same id.
func guestKey(m *tg.Message) string { return "tg:g:" + m.GuestQueryID }

// Guest takes a guest message with the text the bot read as its prompt, the
// mention of framy taken out, and works it in the background.
func (f *Flow) Guest(ctx context.Context, m *tg.Message, prompt string) {
	if m == nil || m.From == nil || m.GuestQueryID == "" {
		return
	}
	f.wg.Go(func() {
		f.withSlot(ctx, func() {
			if err := f.guest(ctx, m, strings.TrimSpace(prompt)); err != nil && ctx.Err() == nil {
				f.log.Error("guest message failed", "error", err)
			}
		})
	})
}

func (f *Flow) guest(ctx context.Context, m *tg.Message, prompt string) error {
	lang := f.langOf(ctx, m)
	if f.cfg.AppURL == "" {
		// Telegram fetches every picture of the run from the site.
		f.log.Warn("a guest message with no https site to show it from")
		return nil
	}
	if prompt == "" {
		return f.guestText(ctx, m, t(lang, "guest.empty"))
	}
	key := guestKey(m)
	if _, seen, err := f.cfg.Store.JobByKey(ctx, key); err != nil {
		return err
	} else if seen {
		return nil
	}
	src := guestSources(m)
	if src.clip != nil {
		return f.guestText(ctx, m, t(lang, "guest.video"))
	}
	dir, done, err := f.cfg.Work.Dir("guest")
	if err != nil {
		return f.guestFail(ctx, m, lang, err)
	}
	defer done()
	d, refusal, err := f.draft(ctx, m, src, cutPrompt(prompt), key, dir)
	if err != nil {
		return f.guestFail(ctx, m, lang, err)
	}
	if refusal != "" {
		return f.guestText(ctx, m, refusal)
	}
	if d.id == 0 {
		return nil
	}
	run := db.ChatRun{JobID: d.id, ChatID: m.Chat.ID, Lang: lang, Taken: d.taken, Note: d.note, UserID: m.From.ID}
	if err := f.cfg.Store.AddChatRun(ctx, run); err != nil {
		f.cancel(ctx, d.id)
		return f.guestFail(ctx, m, lang, err)
	}
	// The run is recorded before the answer: Telegram may fetch the card
	// while it is being answered.
	card := previewCard(lang, d)
	result := tg.InlineQueryResultArticle{
		ID: "g" + strconv.FormatInt(d.id, 10), Title: "framy",
		InputMessageContent: tg.InputTextMessageContent{
			MessageText: previewCaption(lang, prompt, d, labelDrawn(card)), LinkPreview: f.guestPreview(run),
		},
		ReplyMarkup: previewKeyboard(lang, d.id, d),
	}
	sent, err := f.cfg.Telegram.AnswerGuestQuery(ctx, m.GuestQueryID, result)
	if err != nil {
		f.cancel(ctx, d.id)
		if _, merr := f.cfg.Store.MoveChatRun(ctx, d.id, db.StagePreview, db.StageGone); merr != nil {
			f.log.Warn("unanswered guest run not closed", "job", d.id, "error", merr)
		}
		return fmt.Errorf("answer the guest message: %w", err)
	}
	if err := f.cfg.Store.SetChatRunInline(ctx, d.id, sent.InlineMessageID); err != nil {
		// Nothing can reach the answer now; the draft expires.
		f.cancel(ctx, d.id)
		return err
	}
	return nil
}

// guestSources is what a guest message works on, as sourcesOf reads a
// command: the photo it replied to, or its own. framy is not in the chat,
// so of an album only the photo answered is taken.
func guestSources(m *tg.Message) found {
	src := sourcesOf(m)
	src.group = ""
	return src
}

// guestText answers a guest message with text: a refusal or a hint. It is
// the one answer the message gets.
func (f *Flow) guestText(ctx context.Context, m *tg.Message, text string) error {
	_, err := f.cfg.Telegram.AnswerGuestQuery(ctx, m.GuestQueryID, tg.InlineQueryResultArticle{
		ID: "t", Title: "framy", InputMessageContent: tg.InputTextMessageContent{MessageText: text},
	})
	return err
}

// GuestOpen answers a guest message that replied to framy's answer while
// that one still has buttons: the buttons are how it goes on, and a second
// run off the same words would only crowd the chat.
func (f *Flow) GuestOpen(ctx context.Context, m *tg.Message) {
	if m == nil || m.From == nil || m.GuestQueryID == "" {
		return
	}
	f.wg.Go(func() {
		if err := f.guestText(ctx, m, t(f.langOf(ctx, m), "guest.open")); err != nil && ctx.Err() == nil {
			f.log.Warn("guest hint not sent", "error", err)
		}
	})
}

// guestFail tells a guest message it could not be worked; the error goes on
// to the log.
func (f *Flow) guestFail(ctx context.Context, m *tg.Message, lang string, err error) error {
	if rerr := f.guestText(ctx, m, errorText(lang, err)); rerr != nil {
		f.log.Warn("guest failure not told", "error", rerr)
	}
	return err
}

// guest tells whether a run is a guest one.
func guest(run db.ChatRun) bool { return run.InlineMessageID != "" }

// guestURL is a link of the run's for Telegram to fetch. The card changes
// with the run, and Telegram may keep what it fetched by the link, so each
// link is new.
func (f *Flow) guestURL(run db.ChatRun, part byte) string {
	return f.shareURL(run.UserID, run.JobID, part) + "?v=" + strconv.FormatInt(f.shareSeq.Add(1), 10)
}

// guestPicture is a picture the anim key names by the file_id Telegram has
// of it, else by the link framy serves it at.
func (f *Flow) guestPicture(pic anim.Media, link string) tg.InputFile {
	if pic.Key != "" {
		if id, ok := f.files.Load(pic.Key); ok {
			return tg.InputFileString{Data: id.(string)}
		}
	}
	return tg.InputFileString{Data: link}
}

// guestPreview shows the run's card, as it is now, above the answer's text.
func (f *Flow) guestPreview(run db.ChatRun) *tg.LinkPreviewOptions {
	return &tg.LinkPreviewOptions{URL: f.guestURL(run, shareCard), PreferLargeMedia: true, ShowAboveText: true}
}

// guestCard puts the preview's text on the answer with kb, and the card as
// it is now above it.
func (f *Flow) guestCard(ctx context.Context, run db.ChatRun, caption string, kb *tg.InlineKeyboardMarkup) error {
	return f.cfg.Telegram.EditInlineMessageTextWithPreview(ctx, run.InlineMessageID, caption, f.guestPreview(run), kb)
}

// guestLoop turns the answer into the generating loop, with Stop; failing
// that, into the card, which still says the run is on.
func (f *Flow) guestLoop(ctx context.Context, run db.ChatRun) {
	kb := generatingKeyboard(run.Lang, run.JobID)
	caption := generatingCaption(run.Lang, run.Prompt, run.Model, 0)
	loop, err := f.cfg.Renderer.Generating(ctx)
	if err == nil {
		item := tg.InputMediaAnimation{Media: f.guestPicture(loop, f.guestURL(run, shareLoop)), Caption: caption,
			Width: loop.Width, Height: loop.Height, Duration: seconds(loop.Duration)}
		err = f.cfg.Telegram.EditInlineMessageMedia(ctx, run.InlineMessageID, item, kb)
	}
	if err == nil {
		return
	}
	f.log.Warn("generating loop not shown", "job", run.JobID, "error", err)
	if err := f.guestStill(ctx, run, caption, kb); err != nil {
		f.log.Warn("card not shown for the loop", "job", run.JobID, "error", err)
		f.guestCaption(ctx, run, caption, kb)
	}
}

// guestStill puts the run's card on the answer as a picture.
func (f *Flow) guestStill(ctx context.Context, run db.ChatRun, caption string, kb *tg.InlineKeyboardMarkup) error {
	item := tg.InputMediaPhoto{Media: tg.InputFileString{Data: f.guestURL(run, shareCard)}, Caption: caption}
	return f.cfg.Telegram.EditInlineMessageMedia(ctx, run.InlineMessageID, item, kb)
}

// guestCaption changes only the answer's words and buttons: the caption of
// the picture it is, or its text while it is still one.
func (f *Flow) guestCaption(ctx context.Context, run db.ChatRun, caption string, kb *tg.InlineKeyboardMarkup) {
	err := f.cfg.Telegram.EditInlineMessageCaption(ctx, run.InlineMessageID, caption, tg.EditCaptionOptions{Markup: kb})
	if err == nil || tg.IsMessageNotModified(err) {
		return
	}
	if terr := f.cfg.Telegram.EditInlineMessageTextWithPreview(ctx, run.InlineMessageID, caption, nil, kb); terr != nil && !tg.IsMessageNotModified(terr) {
		f.log.Warn("guest caption not changed", "job", run.JobID, "error", err, "as_text", terr)
	}
}

// guestEnd is the end of a guest run that leaves nothing: No or an expired
// preview, or Stop on the loop. An inline message cannot be deleted: the
// preview, still text, shrinks to one line; the loop gives way to the card
// it started from.
func (f *Flow) guestEnd(ctx context.Context, run db.ChatRun) {
	if run.Stage == db.StagePreview {
		err := f.cfg.Telegram.EditInlineMessageTextWithPreview(ctx, run.InlineMessageID, t(run.Lang, "guest.cancelled"), nil, nil)
		if err != nil && !tg.IsMessageNotModified(err) {
			f.log.Warn("guest preview not closed", "job", run.JobID, "error", err)
		}
		return
	}
	caption := lines(promptQuote(run.Prompt), t(run.Lang, "guest.stopped"))
	if err := f.guestStill(ctx, run, caption, nil); err != nil {
		f.log.Warn("card not shown for the stopped loop", "job", run.JobID, "error", err)
		f.guestCaption(ctx, run, caption, nil)
	}
}

// guestDeliver puts the result on the answer: a picture as a JPEG framy
// makes, a video as it is kept. What Telegram cannot fetch, or will not
// show, stays in the person's history, and the answer says where.
func (f *Flow) guestDeliver(ctx context.Context, run db.ChatRun, a storage.Asset) {
	caption := resultCaption(run.Prompt, run.Model)
	var item tg.InputMedia
	switch {
	case a.ContentType == "video/mp4" && a.Bytes <= shareVideoBytes:
		w, h := fit(a.Width, a.Height, 0)
		item = tg.InputMediaVideo{Media: tg.InputFileString{Data: f.shareURL(run.UserID, a.ID, shareVideo)}, Caption: caption,
			Width: w, Height: h, Duration: int(a.Seconds + 0.5), SupportsStreaming: true}
	case shareable(a.ContentType):
		item = tg.InputMediaPhoto{Media: tg.InputFileString{Data: f.shareURL(run.UserID, a.ID, sharePhoto)}, Caption: caption}
	}
	var err error
	if item != nil {
		err = f.cfg.Telegram.EditInlineMessageMedia(ctx, run.InlineMessageID, item, nil)
	}
	if item == nil || err != nil {
		if err != nil {
			f.log.Warn("guest result not shown; the history has it", "job", run.JobID, "error", err)
		}
		f.guestCaption(ctx, run, lines(caption, t(run.Lang, "guest.elsewhere")), nil)
	}
	if _, err := f.cfg.Store.FinishChatRun(ctx, run.JobID, db.StageDone, db.ChatResult{}); err != nil {
		f.log.Error("run not finished", "job", run.JobID, "error", err)
		return
	}
	f.forget(run.JobID)
}

// guestFailed puts the error card on the answer, or its line in the
// caption when Telegram does not take the card.
func (f *Flow) guestFailed(ctx context.Context, run db.ChatRun, key string) {
	text := t(run.Lang, key)
	model := "<b>" + html.EscapeString(run.Model) + "</b>"
	card, err := f.cfg.Renderer.ErrorCard(ctx, text, run.Lang)
	if err == nil {
		line := ""
		if !card.TextDrawn {
			line = text
		}
		item := tg.InputMediaPhoto{Media: f.guestPicture(card, f.guestURL(run, shareFailure)), Caption: lines(promptQuote(run.Prompt), line, model)}
		err = f.cfg.Telegram.EditInlineMessageMedia(ctx, run.InlineMessageID, item, nil)
	}
	if err != nil {
		f.log.Warn("error card not shown; the caption says it", "job", run.JobID, "error", err)
		f.guestCaption(ctx, run, lines(promptQuote(run.Prompt), text, model), nil)
	}
	if _, err := f.cfg.Store.FinishChatRun(ctx, run.JobID, db.StageFailed, db.ChatResult{}); err != nil {
		f.log.Error("run not finished", "job", run.JobID, "error", err)
		return
	}
	f.forget(run.JobID)
}

// ---- the links of a guest run ----

// guestPart tells whether a link's part is one of a guest run's.
func guestPart(part byte) bool {
	return part == shareCard || part == shareLoop || part == shareFailure
}

// serveGuest serves a part of guest run jobID of userID's: the loop, which
// is every run's, or a card drawn as the run is now.
func (f *Flow) serveGuest(w http.ResponseWriter, r *http.Request, userID, jobID int64, part byte) {
	ctx := r.Context()
	if part == shareLoop {
		loop, err := f.cfg.Renderer.Generating(ctx)
		if err != nil {
			f.log.Warn("loop not served", "error", err)
			http.Error(w, "not made", http.StatusServiceUnavailable)
			return
		}
		serveFile(w, r, loop.Path, "video/mp4")
		return
	}
	run, err := f.cfg.Store.ChatRun(ctx, jobID)
	if err != nil || run.UserID != userID {
		http.NotFound(w, r)
		return
	}
	if !f.takeShareSlot(w, r) {
		return
	}
	defer func() { <-f.shareSlots }()
	var pic anim.Media
	dir, done, err := f.cfg.Work.Dir("guest")
	if err == nil {
		defer done()
		switch part {
		case shareFailure:
			pic, err = f.cfg.Renderer.ErrorCard(ctx, t(run.Lang, reason(run)), run.Lang)
		default:
			pic, err = f.runCard(ctx, run, dir)
		}
	}
	var out []byte
	if err == nil {
		out, err = jpegOfFile(pic.Path, guestCardSide)
	}
	if err != nil {
		if ctx.Err() == nil {
			f.log.Warn("guest card not made", "job", jobID, "part", string(part), "error", err)
			http.Error(w, "not made", http.StatusUnprocessableEntity)
		}
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	_, _ = w.Write(out)
}

// runCard draws the preview card of run as it is now, from its files.
func (f *Flow) runCard(ctx context.Context, run db.ChatRun, dir string) (anim.Media, error) {
	s, err := f.stateOf(ctx, run)
	if err != nil {
		return anim.Media{}, err
	}
	paths, err := f.cardPictures(ctx, dir, s)
	if err != nil {
		return anim.Media{}, err
	}
	return f.cfg.Renderer.Preview(ctx, dir, previewCard(run.Lang, s.draft), paths)
}

// jpegOfFile is the picture at path as jpegOf makes it.
func jpegOfFile(path string, side int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("read the card: %w", err)
	}
	return jpegOf(img, side)
}

// serveFile serves the file at path as contentType.
func serveFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "not made", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", contentType)
	if st, err := file.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	_, _ = io.Copy(w, file)
}
