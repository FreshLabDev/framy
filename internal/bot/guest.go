// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"regexp"
	"strings"

	"github.com/FreshLabDev/tg"
)

// onGuest passes a guest message to the flow (docs/design.md, Guest mode).
// Another bot's is left alone: two guest bots could summon each other.
func (b *Bot) onGuest(ctx context.Context, m *tg.Message) {
	if b.flow == nil || m.From == nil || m.From.IsBot || m.GuestQueryID == "" {
		return
	}
	b.flow.Guest(ctx, m, guestPrompt(m, b.username))
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
