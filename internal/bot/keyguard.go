// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"fmt"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// keyInChat deletes a direct message that looks like a provider key the
// moment it arrives, or is edited to hold one, and answers where keys go: the
// settings of the site or the Mini App, which its button opens at the key's
// provider. Nothing from the message is kept, logged or counted but the fact
// that one came. It reports whether the message was one. A command or a
// guest message elsewhere is stopped by the flow (internal/chat,
// keyguard.go).
//
// The person is told to treat the key as exposed: it has been through
// Telegram and the Bot API server, and deleting it here does not take it back.
// When Telegram refuses the delete, the answer says so and asks the person to
// delete it, rather than claiming what did not happen.
func (b *Bot) keyInChat(ctx context.Context, m *tg.Message) (handled bool, err error) {
	if m.Chat.Type != "private" || m.From == nil || m.From.IsBot {
		return false, nil
	}
	provider, ok := keys.KeyIn(m.Text, m.Caption)
	if !ok {
		return false, nil
	}
	metrics.KeysInChat.Inc()
	answer := "msg.key_deleted"
	if err := b.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
		answer = "msg.key_kept"
		b.log.Error("a message that looks like a key could not be deleted", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
	}
	raw, langErr := b.personLang(ctx, *m.From)
	if langErr != nil {
		raw = m.From.LanguageCode
	}
	lang := i18n.Resolve(raw)
	var kb *tg.InlineKeyboardMarkup
	if b.appURL != "" {
		label := i18n.T(lang, "btn.open")
		if provider != "" {
			label = i18n.T(lang, "web.go.connect", "provider", connectionName(provider))
		}
		kb = markup(row{{Text: label, WebApp: &tg.WebAppInfo{URL: keys.SettingsURL(b.appURL, provider)}, Style: tg.StylePrimary}})
	}
	if _, err := b.tg.SendMessage(ctx, m.Chat.ID, i18n.T(lang, answer), kb); err != nil {
		return true, fmt.Errorf("answer a key in %d: %w", m.Chat.ID, err)
	}
	return true, nil
}
