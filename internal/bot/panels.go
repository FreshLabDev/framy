// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// command returns the command a message carries, without the slash, when it
// is meant for framy. In a group a bare /start is for every bot there, and
// /start@another is not ours.
func (b *Bot) command(m *tg.Message) (string, bool) {
	cmd, _, ok := b.commandArgs(m)
	return cmd, ok
}

// commandArgs is command with the text after it. A photo's caption carries a
// command as well: /img as the caption of one's own photo edits that photo.
func (b *Bot) commandArgs(m *tg.Message) (cmd, rest string, ok bool) {
	if m.From == nil || m.From.IsBot {
		return "", "", false
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	text = strings.TrimSpace(text)
	head, rest, _ := strings.Cut(text, " ")
	// "/img\na fox" is a command too.
	if h, r, found := strings.Cut(head, "\n"); found {
		head, rest = h, r+" "+rest
	}
	if !strings.HasPrefix(head, "/") {
		return "", "", false
	}
	cmd, at, addressed := strings.Cut(head[1:], "@")
	if addressed && !strings.EqualFold(at, b.username) {
		return "", "", false
	}
	return strings.ToLower(cmd), strings.TrimSpace(rest), cmd != ""
}

func (b *Bot) onMessage(ctx context.Context, m *tg.Message) error {
	if handled, err := b.keyInChat(ctx, m); handled {
		return err
	}
	cmd, rest, ok := b.commandArgs(m)
	private := m.Chat.Type == "private"
	if !ok {
		// In a direct chat no command is needed (docs/design.md, DM).
		if private && b.flow != nil && m.From != nil && !m.From.IsBot && !strings.HasPrefix(strings.TrimSpace(m.Text), "/") {
			b.flow.Plain(ctx, m)
		}
		return nil
	}
	switch cmd {
	case "img", "edit":
		if b.flow != nil {
			b.flow.Command(ctx, m, cmd, rest)
		}
		return nil
	case "start":
		// In a group /start is registered as ephemeral, so the panel is seen
		// by the one who asked. An ordinary /start there would put one
		// person's menu in everyone's feed, and stays unanswered.
		if !private && m.EphemeralMessageID == 0 {
			return nil
		}
		return b.sendScreen(ctx, m, actHome)
	case "help", "about", "language":
		// Tabs of the direct chat's panel. They answer for anyone who types
		// them from memory and are not advertised.
		if !private {
			return nil
		}
		action := cmd
		if cmd == "language" {
			action = actLang
		}
		return b.sendScreen(ctx, m, action)
	}
	return nil
}

// sendScreen answers a command with a panel: in place in a direct chat, as an
// ephemeral reply in a group.
func (b *Bot) sendScreen(ctx context.Context, m *tg.Message, action string) error {
	raw, err := b.personLang(ctx, *m.From)
	if err != nil {
		return err
	}
	v := view{lang: i18n.Resolve(raw), owner: m.From.ID, sc: scopeOf(m.Chat)}
	var text string
	var keyboard *tg.InlineKeyboardMarkup
	if v.sc == scopeGroup {
		chatLang, panelLang, err := b.groupLang(ctx, m.Chat.ID, raw)
		if err != nil {
			return err
		}
		v.lang = panelLang
		admin, err := b.isAdmin(ctx, m.Chat.ID, m.From.ID)
		if err != nil {
			// The member's panel is the safe one to show.
			b.log.Warn("admin check failed", "chat", m.Chat.ID, "error", err)
		}
		text, keyboard = v.home(admin, chatLang)
		if _, err := b.tg.SendEphemeralMessage(ctx, m.Chat.ID, m.From.ID, m.EphemeralMessageID, text, keyboard); err != nil {
			return fmt.Errorf("send ephemeral home to %d: %w", m.Chat.ID, err)
		}
		return nil
	}
	switch action {
	case actHelp:
		text, keyboard = v.help()
	case actAbout:
		text, keyboard = v.about(b.version)
	case actLang:
		text, keyboard = v.language()
	default:
		v.account = b.readAccount(ctx, m.From.ID, v.lang)
		text, keyboard = v.home(false, "")
	}
	if _, err := b.tg.SendMessage(ctx, m.Chat.ID, text, keyboard); err != nil {
		return fmt.Errorf("send %s to %d: %w", action, m.Chat.ID, err)
	}
	return nil
}

// personLang is what core resolves for the person, or their client's language
// while core has nothing.
func (b *Bot) personLang(ctx context.Context, u tg.User) (string, error) {
	lang, err := b.store.UserLang(ctx, u.ID)
	if err != nil {
		return "", err
	}
	if lang == "" {
		lang = u.LanguageCode
	}
	return lang, nil
}

// groupLang is the group's language in core, and the language a panel there
// is drawn in: the group's, or the reader's while the group has none, so a
// panel is never drawn in a language nobody asked for.
func (b *Bot) groupLang(ctx context.Context, chatID int64, reader string) (chat, panelLang string, err error) {
	chat, err = b.store.ChatLang(ctx, chatID)
	if err != nil {
		return "", "", err
	}
	if chat != "" {
		return chat, i18n.Resolve(chat), nil
	}
	return "", i18n.Resolve(reader), nil
}

// isAdmin asks Telegram on every use: rights change, and nothing is cached.
func (b *Bot) isAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	member, err := b.tg.GetChatMember(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	return member.Status == "administrator" || member.Status == "creator", nil
}

// addressable reports whether a callback may act on the message it came from.
// Telegram lets a client send any callback data against any message it can
// see, and the owner id stops only one person driving another's panel. In a
// group only an ephemeral panel is addressable: it is already one person's.
// In a direct chat every message is the person's own, and a panel is text.
func addressable(m tg.Message) bool {
	if m.Chat.ID == 0 {
		return false
	}
	if m.Chat.Type != "private" {
		return m.EphemeralMessageID != 0
	}
	return m.MessageID != 0 && m.Text != ""
}

func (b *Bot) onCallback(ctx context.Context, cq *tg.CallbackQuery) error {
	// The only buttons framy puts on an inline message are a guest run's:
	// a press there is none of its panels.
	if cq.Message == nil {
		if b.flow != nil && cq.InlineMessageID != "" && b.flow.Owns(cq.Data) {
			b.flow.Callback(ctx, cq)
			return nil
		}
		return b.tg.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	if b.flow != nil && b.flow.Owns(cq.Data) {
		b.flow.Callback(ctx, cq)
		return nil
	}
	owner, action, ok := parseMenuCB(cq.Data)
	if !ok {
		return b.tg.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	raw, err := b.personLang(ctx, cq.From)
	if err != nil {
		_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	if cq.From.ID != owner {
		return b.tg.AnswerCallbackQueryAlert(ctx, cq.ID, i18n.T(i18n.Resolve(raw), "msg.not_yours"))
	}
	if !addressable(*cq.Message) {
		return b.tg.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	alert, err := b.runAction(ctx, cq, action, raw)
	if alert != "" {
		return b.tg.AnswerCallbackQueryAlert(ctx, cq.ID, alert)
	}
	// Also after a failure: a button that spins for half a minute is worse
	// than one that does nothing.
	if answerErr := b.tg.AnswerCallbackQuery(ctx, cq.ID, ""); err == nil {
		err = answerErr
	}
	return err
}

// runAction does what the button says and redraws the panel. It returns an
// alert to show instead when the press is refused.
func (b *Bot) runAction(ctx context.Context, cq *tg.CallbackQuery, action, raw string) (string, error) {
	m := cq.Message
	v := view{lang: i18n.Resolve(raw), owner: cq.From.ID, sc: scopeOf(m.Chat)}
	ephemeral := m.EphemeralMessageID != 0
	if action == actClose {
		if v.sc != scopeGroup {
			return "", nil
		}
		return "", b.tg.DeleteEphemeralMessage(ctx, m.Chat.ID, cq.From.ID, m.EphemeralMessageID)
	}
	edit := func(text string, keyboard *tg.InlineKeyboardMarkup) (string, error) {
		if ephemeral {
			return "", b.tg.EditEphemeralMessageText(ctx, m.Chat.ID, cq.From.ID, m.EphemeralMessageID, text, keyboard)
		}
		return "", b.tg.EditMessageText(ctx, m.Chat.ID, m.MessageID, text, keyboard)
	}

	var chatLang string
	admin := false
	if v.sc == scopeGroup {
		var err error
		if chatLang, v.lang, err = b.groupLang(ctx, m.Chat.ID, raw); err != nil {
			return "", err
		}
		if adminOnly(action) || action == actHome {
			if admin, err = b.isAdmin(ctx, m.Chat.ID, cq.From.ID); err != nil {
				return "", err
			}
		}
		if adminOnly(action) && !admin {
			return i18n.T(i18n.Resolve(raw), "msg.admin_only"), nil
		}
	}

	switch action {
	case actHome:
		if v.sc == scopePrivate {
			v.account = b.readAccount(ctx, cq.From.ID, v.lang)
		}
		return edit(v.home(admin, chatLang))
	case actHelp:
		return edit(v.help())
	case actAbout:
		return edit(v.about(b.version))
	case actLang:
		if v.sc != scopePrivate {
			return "", nil
		}
		return edit(v.language())
	case actLangAuto:
		if v.sc != scopePrivate {
			return "", nil
		}
		if err := b.store.ClearUserLang(ctx, cq.From.ID); err != nil {
			return "", err
		}
		// Ask core again rather than assume the client's code: another bot
		// may still hold a manual pick, and then it wins.
		raw, err := b.personLang(ctx, cq.From)
		if err != nil {
			return "", err
		}
		v.lang = i18n.Resolve(raw)
		return edit(v.language())
	case actGroupLang:
		return edit(v.groupLanguage(chatLang))
	case actGroupAuto:
		if err := b.store.ClearChatLang(ctx, m.Chat.ID); err != nil {
			return "", err
		}
		// Core may still hold an automatic language for the group.
		var err error
		if chatLang, v.lang, err = b.groupLang(ctx, m.Chat.ID, raw); err != nil {
			return "", err
		}
		return edit(v.groupLanguage(chatLang))
	}

	if code, ok := strings.CutPrefix(action, actLang+":"); ok {
		// The person's own language is set from their direct chat, where it
		// is offered; in a group the language there is the group's.
		if !i18n.IsSupported(code) || v.sc != scopePrivate {
			return "", nil
		}
		if err := b.store.SetUserLang(ctx, cq.From.ID, code); err != nil {
			return "", err
		}
		v.lang = code
		return edit(v.language())
	}
	if code, ok := strings.CutPrefix(action, actGroupLang+":"); ok {
		if !i18n.IsSupported(code) || v.sc != scopeGroup {
			return "", nil
		}
		if err := b.store.SetChatLang(ctx, m.Chat.ID, code); err != nil {
			return "", err
		}
		v.lang = code
		return edit(v.groupLanguage(code))
	}
	return "", nil
}
