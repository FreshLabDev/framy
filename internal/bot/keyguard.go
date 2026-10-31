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
// settings of the site or the Mini App. Nothing from the message is kept, logged or counted but the
// fact that one came. It reports whether the message was one.
//
// The person is told to treat the key as exposed: it has been through
// Telegram and the Bot API server, and deleting it here does not take it back.
// When Telegram refuses the delete, the answer says so and asks the person to
// delete it, rather than claiming what did not happen.
func (b *Bot) keyInChat(ctx context.Context, m *tg.Message) (handled bool, err error) {
	if m.Chat.Type != "private" || m.From == nil || m.From.IsBot || !keys.LooksLikeKey(m.Text, m.Caption) {
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
	if _, err := b.tg.SendMessage(ctx, m.Chat.ID, i18n.T(i18n.Resolve(raw), answer), nil); err != nil {
		return true, fmt.Errorf("answer a key in %d: %w", m.Chat.ID, err)
	}
	return true, nil
}
