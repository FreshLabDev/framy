// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// keyInCommand stops a command or a guest message that holds something
// shaped like a provider key, before anything of it is kept, priced or sent
// to a model: a person who pastes a key after /img in a group most likely
// meant to connect it, and as a prompt it would go to the provider, into
// the job and into the preview's caption that the whole chat reads. It
// reports whether the message was one.
//
// The command is deleted where framy may delete it. A guest's chat is not
// framy's, and an ephemeral command has no message to delete, so the answer
// asks the person to delete it. The answer is the bot's answer to such a
// direct message (internal/bot, keyguard.go), with the way to the settings
// at the key's provider under it. Nothing of the message is logged.
func (f *Flow) keyInCommand(ctx context.Context, m *tg.Message, prompt string) (bool, error) {
	provider, ok := keys.KeyIn(m.Text, m.Caption, prompt)
	if !ok {
		return false, nil
	}
	metrics.KeysInChat.Inc()
	lang := f.langOf(ctx, m)
	guestMsg := m.GuestQueryID != ""
	answer := "msg.key_kept"
	if !guestMsg && m.MessageID != 0 {
		if err := f.cfg.Telegram.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			f.log.Warn("a command that looks like a key could not be deleted", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
		} else {
			answer = "msg.key_deleted"
		}
	}
	label := t(lang, "btn.open")
	if provider != "" {
		label = t(lang, "web.go.connect", "provider", providerName(provider))
	}
	direct := !guestMsg && m.Chat.Type == "private"
	var kb *tg.InlineKeyboardMarkup
	if b, ok := f.keyButton(lang, direct, provider, label); ok {
		kb = &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{b}}}
	}
	if guestMsg {
		return true, f.guestAnswer(ctx, m, t(lang, answer), kb)
	}
	if answer == "msg.key_deleted" {
		// What it answers is gone: Telegram sends an ephemeral reply only to
		// a message that is there, and a reply to none goes on its own.
		gone := *m
		gone.MessageID = 0
		m = &gone
	}
	return true, f.replyWith(ctx, m, t(lang, answer), kb)
}
