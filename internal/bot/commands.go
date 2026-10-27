// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"net/url"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// publishProfile sets the command menu and the profile texts in every
// language, and once more without a language for clients outside the sixteen.
// A failure is logged and polling goes on: the commands work without their
// descriptions.
//
// The direct chat's menu is one entry: help, about and language are tabs of
// the /start panel, and a command that only opens a tab is the same door
// listed twice. In a group /start is ephemeral, so the panel is seen only by
// the one who asked.
func (b *Bot) publishProfile(ctx context.Context) {
	for _, code := range append([]string{""}, i18n.Codes()...) {
		lang := code
		if lang == "" {
			lang = i18n.DefaultLang
		}
		private := []tg.BotCommand{{Command: "start", Description: i18n.T(lang, "cmd.start")}}
		if err := b.tg.SetMyCommandsForScope(ctx, private, &tg.BotCommandScope{Type: "all_private_chats", LanguageCode: code}); err != nil {
			b.log.Warn("bot commands not published", "scope", "all_private_chats", "language", code, "error", err)
			return
		}
		group := []tg.BotCommand{{Command: "start", Description: i18n.T(lang, "cmd.start_group"), IsEphemeral: true}}
		if err := b.tg.SetMyCommandsForScope(ctx, group, &tg.BotCommandScope{Type: "all_group_chats", LanguageCode: code}); err != nil {
			b.log.Warn("bot commands not published", "scope", "all_group_chats", "language", code, "error", err)
			return
		}
		if err := b.setDescriptions(ctx, code, i18n.T(lang, "bot.description"), i18n.T(lang, "bot.short")); err != nil {
			b.log.Warn("bot description not published", "language", code, "error", err)
			return
		}
	}
	b.log.Info("bot commands and descriptions published")
}

// publishMenuButton makes the button beside the input field of every direct
// chat open the site as a Mini App. The label is the brand name, the same in
// every language. Without a site address it does nothing, and does not reset a
// button someone set by hand. A failure is logged: the button is a shortcut to
// what the panel's own button opens.
func (b *Bot) publishMenuButton(ctx context.Context) {
	if b.appURL == "" {
		return
	}
	button := tg.MenuButton{Type: tg.MenuWebApp, Text: productName, WebApp: &tg.WebAppInfo{URL: b.appURL}}
	if err := b.tg.SetChatMenuButton(ctx, 0, button); err != nil {
		b.log.Warn("menu button not published", "error", err)
		return
	}
	b.log.Info("menu button published")
}

// setDescriptions writes the profile texts only where they differ from what
// Telegram holds: a restart then costs reads, not writes, and Telegram limits
// how often these change.
//
// tg has the setters; the getters still go through CallGet.
func (b *Bot) setDescriptions(ctx context.Context, lang, description, short string) error {
	for _, d := range []struct {
		get, field, text string
		set              func(ctx context.Context, text, lang string) error
	}{
		{"getMyDescription", "description", description, b.tg.SetMyDescription},
		{"getMyShortDescription", "short_description", short, b.tg.SetMyShortDescription},
	} {
		q := url.Values{}
		if lang != "" {
			q.Set("language_code", lang)
		}
		var cur struct {
			OK     bool              `json:"ok"`
			Result map[string]string `json:"result"`
		}
		if err := b.tg.CallGet(ctx, d.get, q, &cur); err == nil && cur.OK && cur.Result[d.field] == d.text {
			continue
		}
		if err := d.set(ctx, d.text, lang); err != nil {
			return err
		}
	}
	return nil
}
