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

// setDescriptions writes the profile texts only where they differ from what
// Telegram holds: a restart then costs reads, not writes, and Telegram limits
// how often these change.
func (b *Bot) setDescriptions(ctx context.Context, lang, description, short string) error {
	for _, d := range []struct{ get, set, field, text string }{
		{"getMyDescription", "setMyDescription", "description", description},
		{"getMyShortDescription", "setMyShortDescription", "short_description", short},
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
		req := map[string]any{d.field: d.text}
		if lang != "" {
			req["language_code"] = lang
		}
		var res struct {
			OK bool `json:"ok"`
		}
		if err := b.tg.Call(ctx, d.set, req, &res); err != nil {
			return err
		}
	}
	return nil
}
