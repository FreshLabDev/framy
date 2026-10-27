// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/price"
)

// Captions are at most 1024 characters, so a long prompt is cut in the
// caption; the job keeps it whole.
const (
	captionPrompt = 600 // characters of the prompt shown
	// expandAfter is the length from which the prompt folds into an
	// expandable quote.
	expandAfter = 200
)

// providerNames are the brands, written the same in every language.
var providerNames = map[string]string{
	catalog.OpenRouter: "OpenRouter",
	catalog.OpenAI:     "OpenAI",
	catalog.Gemini:     "Google Gemini",
}

// promptQuote is the prompt as the caption shows it.
func promptQuote(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if utf8.RuneCountInString(prompt) > captionPrompt {
		prompt = strings.TrimSpace(string([]rune(prompt)[:captionPrompt])) + "…"
	}
	open := "<blockquote>"
	if utf8.RuneCountInString(prompt) > expandAfter || strings.Count(prompt, "\n") > 2 {
		open = "<blockquote expandable>"
	}
	return open + html.EscapeString(prompt) + "</blockquote>"
}

// lines joins the parts of a caption that are there.
func lines(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// modelLine is the model and what was taken.
func modelLine(lang, model string, taken int) string {
	s := "<b>" + html.EscapeString(model) + "</b>"
	if taken > 0 {
		s += " · " + t(lang, "run.taken", "n", strconv.Itoa(taken))
	}
	return s
}

// noteLine is the note about what was taken, in italics.
func noteLine(lang, note string, taken, album int) string {
	switch note {
	case "":
		return ""
	case noteCut:
		return "<i>" + t(lang, note, "max", strconv.Itoa(taken), "left", strconv.Itoa(max(album-taken, 0))) + "</i>"
	}
	return "<i>" + t(lang, note, "n", strconv.Itoa(taken)) + "</i>"
}

// priceLine is what the run costs the person: the free tier left, or the
// price on their own key.
func priceLine(lang, pay, provider string, usage free.Meter, e price.Estimate, q fx.Quote) string {
	if pay == payFree {
		return t(lang, "run.free", "left", strconv.Itoa(usage.Left()), "limit", strconv.Itoa(usage.Limit))
	}
	name := providerNames[provider]
	if name == "" {
		name = provider
	}
	return t(lang, "run.on_key", "price", priceLabel(lang, e, q), "provider", name)
}

// priceLabel writes an estimate in dollars and, for another currency, in it
// too: "$0.034 · 1,53 ₴".
func priceLabel(lang string, e price.Estimate, q fx.Quote) string {
	amount := e.USD
	key := ""
	switch e.Kind {
	case price.Unknown:
		return t(lang, "run.price_unknown")
	case price.About:
		key = "run.about"
	case price.UpTo:
		amount, key = max(e.MaxUSD, e.USD), "run.up_to"
	case price.From:
		key = "run.from"
	}
	s := fx.Format(amount, fx.USD, lang)
	if q.Code != "" && q.Code != fx.USD && q.PerUSD > 0 {
		s += " · " + fx.Format(amount*q.PerUSD, q.Code, lang)
	}
	if key != "" {
		return t(lang, key, "price", s)
	}
	return s
}

// previewCaption is the caption of a preview.
func previewCaption(lang, prompt string, d draft) string {
	return lines(
		promptQuote(prompt),
		modelLine(lang, d.model.Name, d.taken),
		priceLine(lang, d.pay, d.offer.Provider, d.usage, d.estimate, d.quote),
		noteLine(lang, d.note, d.taken, d.album),
	)
}

// generatingCaption is the caption of the loop: the model and how long the
// run has taken.
func generatingCaption(lang, prompt, model string, elapsed time.Duration) string {
	line := t(lang, "run.generating", "model", "<b>"+html.EscapeString(model)+"</b>")
	if elapsed >= time.Second {
		s := int(elapsed.Seconds())
		line += fmt.Sprintf(" · %d:%02d", s/60, s%60)
	}
	return lines(promptQuote(prompt), line)
}

// resultCaption is the caption of a result.
func resultCaption(prompt, model string) string {
	return lines(promptQuote(prompt), "<b>"+html.EscapeString(model)+"</b>")
}

func button(text, data, style string) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: text, CallbackData: data, Style: style}
}

// previewKeyboard: Generate and No.
func previewKeyboard(lang string, jobID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		button(t(lang, "run.generate"), callback(jobID, actGenerate), "success"),
		button(t(lang, "run.no"), callback(jobID, actNo), "danger"),
	}}}
}

// generatingKeyboard: Stop.
func generatingKeyboard(lang string, jobID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		button(t(lang, "run.stop"), callback(jobID, actStop), ""),
	}}}
}

// sendPreview draws the preview and sends it as a reply to the command: the
// album loop for photos, the still of the generating loop for none.
func (f *Flow) sendPreview(ctx context.Context, m *tg.Message, d draft, prompt, lang string) (int64, error) {
	var pic anim.Media
	var err error
	if len(d.paths) > 0 {
		pic, err = f.cfg.Renderer.Album(ctx, filepath.Dir(d.paths[0]), d.paths)
	} else {
		pic, err = f.cfg.Renderer.Blank(ctx)
	}
	if err != nil {
		return 0, fmt.Errorf("draw the preview: %w", err)
	}
	sent, err := f.send(ctx, m.Chat.ID, m.MessageID, threadOf(m), pic, previewCaption(lang, prompt, d), previewKeyboard(lang, d.id))
	if err != nil {
		return 0, fmt.Errorf("send the preview: %w", err)
	}
	return sent.MessageID, nil
}

// send posts a picture as a reply. A picture with a key (the loop, a card)
// is uploaded once; after that its file_id goes.
func (f *Flow) send(ctx context.Context, chatID, replyTo int64, thread int, pic anim.Media, caption string, kb *tg.InlineKeyboardMarkup) (tg.Message, error) {
	opts := tg.CaptionOptions{ThreadID: thread, ReplyTo: replyTo, Caption: caption, Markup: kb}
	try := func(file tg.InputFile) (tg.Message, error) {
		if isAnimation(pic) {
			return f.cfg.Telegram.SendAnimation(ctx, chatID, file, tg.AnimationOptions{
				CaptionOptions: opts, Width: pic.Width, Height: pic.Height, Duration: seconds(pic.Duration),
			})
		}
		return f.cfg.Telegram.SendPhoto(ctx, chatID, file, opts)
	}
	return f.withFile(pic, try)
}

// edit turns a message into a picture, keeping its place in the chat.
func (f *Flow) edit(ctx context.Context, chatID, messageID int64, pic anim.Media, caption string, kb *tg.InlineKeyboardMarkup) (tg.Message, error) {
	try := func(file tg.InputFile) (tg.Message, error) {
		var item tg.InputMedia
		if isAnimation(pic) {
			item = tg.InputMediaAnimation{Media: file, Caption: caption, Width: pic.Width, Height: pic.Height, Duration: seconds(pic.Duration)}
		} else {
			item = tg.InputMediaPhoto{Media: file, Caption: caption}
		}
		return f.cfg.Telegram.EditMessageMediaMessage(ctx, chatID, messageID, item, kb)
	}
	return f.withFile(pic, try)
}

// withFile calls send with the picture's file_id when Telegram has it, and
// with an upload otherwise or when the id is refused, and keeps the id the
// upload got.
func (f *Flow) withFile(pic anim.Media, send func(tg.InputFile) (tg.Message, error)) (tg.Message, error) {
	if pic.Key != "" {
		if id, ok := f.files.Load(pic.Key); ok {
			msg, err := send(tg.InputFileString{Data: id.(string)})
			if err == nil {
				return msg, nil
			}
			// Telegram may forget a file; the upload below tells.
			f.files.Delete(pic.Key)
		}
	}
	file, err := os.Open(pic.Path)
	if err != nil {
		return tg.Message{}, err
	}
	defer file.Close()
	name := "framy.png"
	if isAnimation(pic) {
		name = "framy.mp4"
	}
	msg, err := send(tg.InputFileUpload{Filename: name, Data: file})
	if err != nil {
		return tg.Message{}, err
	}
	if pic.Key != "" {
		if id := fileIDOf(msg); id != "" {
			f.files.Store(pic.Key, id)
		}
	}
	return msg, nil
}

func isAnimation(pic anim.Media) bool { return pic.ContentType == "video/mp4" }

func seconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }

// fileIDOf is the id of the file a message carries.
func fileIDOf(m tg.Message) string {
	switch {
	case m.Animation != nil:
		return m.Animation.FileID
	case len(m.Photo) > 0:
		return m.Photo[len(m.Photo)-1].FileID
	case m.Video != nil:
		return m.Video.FileID
	case m.Document != nil:
		return m.Document.FileID
	}
	return ""
}

// fileUniqueIDOf is the stable id of the file a message carries.
func fileUniqueIDOf(m tg.Message) string {
	switch {
	case m.Animation != nil:
		return m.Animation.FileUniqueID
	case len(m.Photo) > 0:
		return m.Photo[len(m.Photo)-1].FileUniqueID
	case m.Video != nil:
		return m.Video.FileUniqueID
	case m.Document != nil:
		return m.Document.FileUniqueID
	}
	return ""
}
