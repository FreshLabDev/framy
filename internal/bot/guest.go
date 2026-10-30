// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/FreshLabDev/tg"
)

// onGuest passes a guest message to the flow (docs/design.md, Guest mode).
// Another bot's is left alone: two guest bots could summon each other. A
// reply to framy's answer that still has buttons gets a hint instead of a
// run.
func (b *Bot) onGuest(ctx context.Context, m *tg.Message) {
	if b.flow == nil || m.From == nil || m.From.IsBot || m.GuestQueryID == "" {
		return
	}
	if ownOpen(m.ReplyToMessage, b.selfID) {
		b.flow.GuestOpen(ctx, m)
		return
	}
	b.flow.Guest(ctx, m, guestPrompt(m, b.username))
}

// ownOpen tells whether r is framy's message with buttons: an answer whose
// run is not over. Telegram may give framy's answer as framy's own message
// or as the person's sent via framy; the package models neither via_bot nor
// reply_markup, so they are read from Raw.
func ownOpen(r *tg.Message, self int64) bool {
	if r == nil || self == 0 {
		return false
	}
	var raw struct {
		ViaBot *struct {
			ID int64 `json:"id"`
		} `json:"via_bot"`
		ReplyMarkup *struct {
			InlineKeyboard [][]json.RawMessage `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	if len(r.Raw) == 0 || json.Unmarshal(r.Raw, &raw) != nil {
		return false
	}
	own := (r.From != nil && r.From.ID == self) || (raw.ViaBot != nil && raw.ViaBot.ID == self)
	return own && raw.ReplyMarkup != nil && len(raw.ReplyMarkup.InlineKeyboard) > 0
}

// spaces are the runs of blanks a mention taken out leaves.
var spaces = regexp.MustCompile(`[ \t]{2,}`)

// guestPrompt is the text of a guest message without framy's mention: the
// person writes "@framyin_bot make it snow", or replies to framy's message
// with no mention at all. Mentions of other bots stay: up to three guest
// bots may be summoned at once, and the words are the person's.
func guestPrompt(m *tg.Message, username string) string {
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if username != "" {
		mention := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(username) + `\b`)
		text = mention.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(spaces.ReplaceAllString(text, " "))
}
