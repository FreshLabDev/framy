// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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
	return b.parseCommand(m)
}

// parseCommand is commandArgs whoever sent the message.
func (b *Bot) parseCommand(m *tg.Message) (cmd, rest string, ok bool) {
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
	if asChat, forwarded := sentAsChat(m); asChat {
		// No person sent it, as far as Telegram says, so no run can be
		// anyone's: /img, /edit and the mention are told why. A channel's
		// post its discussion group gets asks for nothing.
		if !forwarded && b.flow != nil && b.asksFramy(m) {
			b.flow.Anonymous(ctx, m)
		}
		return nil
	}
	if handled, err := b.keyInChat(ctx, m); handled {
		return err
	}
	cmd, rest, ok := b.commandArgs(m)
	private := m.Chat.Type == "private"
	if !ok {
		if b.flow == nil || m.From == nil || m.From.IsBot {
			return nil
		}
		command := strings.HasPrefix(strings.TrimSpace(m.Text), "/")
		prompt, named := "", false
		if !private {
			prompt, named = b.mentionArgs(m)
		}
		switch {
		case private && !command:
			// In a direct chat no command is needed (docs/design.md, DM).
			b.flow.Plain(ctx, m)
		case named:
			// "@framyin_bot a fox" in a group framy is in is /img by name,
			// as the same words are in a chat framy is not in (guest mode).
			// It wins over a reply: asked by name, framy takes the replied
			// message as /img does.
			b.flow.Mention(ctx, m, prompt)
		case !private && !command && b.repliesToSelf(m):
			// In a group a reply to framy's result may go on from it.
			b.flow.Reply(ctx, m)
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
		return b.sendScreen(ctx, m, actHome, rest)
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
		return b.sendScreen(ctx, m, action, "")
	}
	return nil
}

// repliesToSelf tells whether m answers one of framy's own messages.
func (b *Bot) repliesToSelf(m *tg.Message) bool {
	r := m.ReplyToMessage
	return r != nil && r.From != nil && b.selfID != 0 && r.From.ID == b.selfID
}

// mentionArgs reads a group message that starts with framy's mention, in
// any case, in its text or caption: the rest is the prompt, with the comma
// or colon after the mention left out. A mention later in the message is
// talk about framy, not to it. A message sent through framy's inline mode
// is a result someone chose to post, not a request.
func (b *Bot) mentionArgs(m *tg.Message) (prompt string, ok bool) {
	if b.username == "" || viaBot(m) {
		return "", false
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	text = strings.TrimSpace(text)
	head := "@" + b.username
	if len(text) < len(head) || !strings.EqualFold(text[:len(head)], head) {
		return "", false
	}
	rest := text[len(head):]
	if r, _ := utf8.DecodeRuneInString(rest); r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
		// "@framyin_bot2": another name.
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(rest, " \t\n,:")), true
}

// sentAsChat tells a group message sent on behalf of a chat, which the
// package does not model: it is read from Raw. An anonymous admin writes as
// the group itself (from is GroupAnonymousBot), and a person may post as a
// channel (Channel_Bot); a channel's post comes to its discussion group as
// an automatic forward, from Telegram. None names the person.
func sentAsChat(m *tg.Message) (asChat, forwarded bool) {
	if len(m.Raw) == 0 || m.Chat.Type == "private" {
		return false, false
	}
	var raw struct {
		SenderChat         *struct{} `json:"sender_chat"`
		IsAutomaticForward bool      `json:"is_automatic_forward"`
	}
	if json.Unmarshal(m.Raw, &raw) != nil || raw.SenderChat == nil {
		return false, false
	}
	return true, raw.IsAutomaticForward
}

// asksFramy tells /img, /edit or framy's mention, whoever sent it.
func (b *Bot) asksFramy(m *tg.Message) bool {
	if cmd, _, ok := b.parseCommand(m); ok {
		return cmd == "img" || cmd == "edit"
	}
	_, ok := b.mentionArgs(m)
	return ok
}

// viaBot tells a message sent through a bot's inline mode, which the
// package does not model: it is read from Raw.
func viaBot(m *tg.Message) bool {
	if len(m.Raw) == 0 {
		return false
	}
	var raw struct {
		ViaBot *struct{} `json:"via_bot"`
	}
	return json.Unmarshal(m.Raw, &raw) == nil && raw.ViaBot != nil
}

// tooLate tells Telegram's refusal of an ephemeral reply to a command it
// no longer takes one for: 15 seconds after the command. A /start sent
// while framy was down is read after that, and its sender sends it again.
func tooLate(err error) bool {
	var apiErr *tg.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == 400 && strings.Contains(apiErr.Description, "REPLY_TO_INVALID")
}

// startSettings starts the /start parameter of the button under a refusal
// or an error card that a key of the person's would fix, in a group or a
// guest's chat (internal/chat, settings.go): "settings_openrouter". The home
// panel it opens has its button at that key in the settings. Messages
// already sent carry it, so it is read for good.
const startSettings = "settings_"

// sendScreen answers a command with a panel: in place in a direct chat, as an
// ephemeral reply in a group. payload is what /start came with.
func (b *Bot) sendScreen(ctx context.Context, m *tg.Message, action, payload string) error {
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
		v.lang, v.direct = panelLang, b.directURL()
		admin, err := b.isAdmin(ctx, m.Chat.ID, m.From.ID)
		if err != nil {
			// The member's panel is the safe one to show.
			b.log.Warn("admin check failed", "chat", m.Chat.ID, "error", err)
		}
		text, keyboard = v.home(admin, chatLang)
		if _, err := b.tg.SendEphemeralMessage(ctx, m.Chat.ID, m.From.ID, m.EphemeralMessageID, text, keyboard); err != nil {
			if tooLate(err) {
				b.log.Info("group /start read too late to answer", "chat", m.Chat.ID)
				return nil
			}
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
		if provider, ok := strings.CutPrefix(payload, startSettings); ok {
			v.account.atKey(provider, v.lang)
		}
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
		} else {
			v.direct = b.directURL()
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
