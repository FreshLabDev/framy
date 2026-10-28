// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"fmt"
	"html"
	"os"
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

// previewCaption is the caption of a preview: the prompt and the note on
// what was taken. The model, the settings and the price are on the card;
// in a language whose letters the card lacks, the price line comes here.
func previewCaption(lang, prompt string, d draft, labelDrawn bool) string {
	price := ""
	if !labelDrawn {
		price = priceLine(lang, d.pay, d.offer.Provider, d.usage, d.estimate, d.quote)
	}
	return lines(promptQuote(prompt), noteLine(lang, d.note, d.taken, d.album), price)
}

// previewCard is what the preview card shows: the model, the settings and
// what the run costs the person.
func previewCard(lang string, d draft) anim.Preview {
	p := anim.Preview{Lang: lang, Maker: d.model.Maker, Model: d.model.Name}
	p.Aspect, p.Chips = settingChips(d.settings)
	if d.pay == payFree {
		p.Free, p.Left, p.Limit = true, d.usage.Left(), d.usage.Limit
		p.Label = t(lang, "card.free")
		return p
	}
	name := providerNames[d.offer.Provider]
	if name == "" {
		name = d.offer.Provider
	}
	p.Label = t(lang, "card.key", "provider", name)
	p.Price, p.Local, p.LocalCode = cardPrice(lang, d.estimate, d.quote)
	if p.Price == "" {
		p.Price = t(lang, "run.price_unknown")
	}
	return p
}

// settingChips are a run's settings as the card shows them: the shape, and
// the rest as short chips. A setting a model has no value for is left out,
// and one result is the rule, so ×1 is not shown.
func settingChips(settings map[string]any) (aspect string, chips []string) {
	if v, ok := settings["aspect_ratio"].(string); ok && v != "auto" {
		aspect = v
	}
	for _, k := range []string{"resolution", "quality"} {
		if v, ok := settings[k]; ok && fmt.Sprint(v) != "" && fmt.Sprint(v) != "auto" {
			chips = append(chips, fmt.Sprint(v))
		}
	}
	if n, ok := settings["n"].(int); ok && n > 1 {
		chips = append(chips, "×"+strconv.Itoa(n))
	}
	return aspect, chips
}

// cardPrice is a price on the person's key as the card writes it, in signs
// rather than words: "≈ $0.034", with "≈ 1,42 ₴" and "≈ 1,42 UAH" for the
// faces that lack ₴. An unknown price is empty.
func cardPrice(lang string, e price.Estimate, q fx.Quote) (usd, local, code string) {
	amount, sign := e.USD, ""
	switch e.Kind {
	case price.Unknown:
		return "", "", ""
	case price.About:
		sign = "≈ "
	case price.UpTo:
		amount, sign = max(e.MaxUSD, e.USD), "≤ "
	case price.From:
		sign = "≥ "
	}
	usd = sign + fx.Format(amount, fx.USD, lang)
	if q.Code != "" && q.Code != fx.USD && q.PerUSD > 0 {
		local = "≈ " + fx.Format(amount*q.PerUSD, q.Code, lang)
		code = "≈ " + fx.FormatCode(amount*q.PerUSD, q.Code, lang)
	}
	return usd, local, code
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

// previewKeyboard: Generate and No, and under them Model and Settings.
func previewKeyboard(lang string, jobID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		button(t(lang, "run.generate"), callback(jobID, actGenerate), "success"),
		button(t(lang, "run.no"), callback(jobID, actNo), "danger"),
	}, {
		button(t(lang, "run.model"), callback(jobID, actModel), ""),
		button(t(lang, "run.settings"), callback(jobID, actSettings), ""),
	}}}
}

// generatingKeyboard: Stop.
func generatingKeyboard(lang string, jobID int64) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		button(t(lang, "run.stop"), callback(jobID, actStop), ""),
	}}}
}

// sendPreview draws the preview card in dir and sends it as a reply to the
// command. In a group it goes to the requester alone when Telegram lets
// framy: framy is an admin, or the command was ephemeral and the answer
// comes within 15 seconds. Otherwise, or when the ephemeral send is
// refused, the chat sees it. ephemeral tells which, and the id is of that
// kind.
func (f *Flow) sendPreview(ctx context.Context, m *tg.Message, d draft, prompt, lang, dir string) (id int64, ephemeral bool, err error) {
	pic, err := f.cfg.Renderer.Preview(ctx, dir, previewCard(lang, d), d.paths)
	if err != nil {
		return 0, false, fmt.Errorf("draw the preview: %w", err)
	}
	opts := tg.CaptionOptions{
		ThreadID: threadOf(m), ReplyTo: m.MessageID,
		Caption: previewCaption(lang, prompt, d, pic.TextDrawn), Markup: previewKeyboard(lang, d.id),
	}
	if m.Chat.Type != "private" {
		switch {
		case ephemeralCommand(m):
			opts.ReplyToEphemeral = m.EphemeralMessageID
			opts.Ephemeral = &tg.Ephemeral{ReceiverUserID: m.From.ID}
		case f.isAdmin(ctx, m.Chat.ID):
			opts.Ephemeral = &tg.Ephemeral{ReceiverUserID: m.From.ID}
		}
	}
	if opts.Ephemeral != nil {
		sent, err := f.sendPicture(ctx, m.Chat.ID, pic, opts)
		if err == nil && sent.EphemeralMessageID != 0 {
			return sent.EphemeralMessageID, true, nil
		}
		if ctx.Err() != nil {
			// A stop cut the send short; Telegram refused nothing, and the
			// preview may have reached the person already. Sending it to the
			// chat now would fail the same way.
			return 0, false, fmt.Errorf("send the preview: %w", ctx.Err())
		}
		f.log.Warn("ephemeral preview refused; the chat sees it", "chat", m.Chat.ID, "error", err)
		opts.Ephemeral, opts.ReplyToEphemeral = nil, 0
	}
	sent, err := f.sendPicture(ctx, m.Chat.ID, pic, opts)
	if err != nil {
		return 0, false, fmt.Errorf("send the preview: %w", err)
	}
	return sent.MessageID, false, nil
}

// isAdmin tells whether framy is an admin of the group, which lets it send
// a member an ephemeral message at any time. The album cache knows: an
// admin sees every message.
func (f *Flow) isAdmin(ctx context.Context, chatID int64) bool {
	if f.albums == nil {
		return false
	}
	admin, err := f.albums.SeesEveryMessage(ctx, chatID)
	if err != nil {
		f.log.Warn("admin rights unread", "chat", chatID, "error", err)
	}
	return admin
}

// send posts a picture as a reply. A picture with a key (the loop, a card)
// is uploaded once; after that its file_id goes.
func (f *Flow) send(ctx context.Context, chatID, replyTo int64, thread int, pic anim.Media, caption string, kb *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return f.sendPicture(ctx, chatID, pic, tg.CaptionOptions{ThreadID: thread, ReplyTo: replyTo, Caption: caption, Markup: kb})
}

// sendPicture posts a picture with the options given.
func (f *Flow) sendPicture(ctx context.Context, chatID int64, pic anim.Media, opts tg.CaptionOptions) (tg.Message, error) {
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
	switch {
	case isAnimation(pic):
		name = "framy.mp4"
	case pic.ContentType == "image/jpeg":
		name = "framy.jpg"
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
