// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// publishProfile sets the command menu and the profile texts in every
// language, and once more without a language for clients outside the sixteen.
// Each language is its own: one Telegram refuses leaves the others to be
// published, and the languages that failed are tried once more a minute
// later (or once Telegram's wait is over, when it asked for a longer one).
// What is still missing then is logged once, by language, and polling goes
// on: the commands work without their descriptions, and a client in a
// missing language shows what it had before, or the texts without a
// language.
//
// The direct chat's menu is one entry: help, about and language are tabs of
// the /start panel, and a command that only opens a tab is the same door
// listed twice. In a group /start is ephemeral, so the panel is seen only by
// the one who asked.
func (b *Bot) publishProfile(ctx context.Context) {
	codes := append([]string{""}, i18n.Codes()...)
	failed, wait, err := b.publishLanguages(ctx, codes)
	if len(failed) > 0 && ctx.Err() == nil {
		b.log.Info("bot profile not published in every language; trying those again", "languages", languageNames(failed), "error", err)
		if b.sleep(ctx, max(profileAgainAfter, wait)) {
			failed, _, err = b.publishLanguages(ctx, failed)
		}
	}
	switch {
	case ctx.Err() != nil:
	case len(failed) > 0:
		b.log.Warn("bot profile not published in every language", "languages", languageNames(failed), "of", len(codes), "error", err)
	default:
		b.log.Info("bot commands and descriptions published")
	}
}

// profileAgainAfter is how long the languages that failed wait for their
// second try.
const profileAgainAfter = time.Minute

// publishLanguages publishes the menus and texts of each language code ("" is
// the one without a language), and returns the codes that failed, the wait
// Telegram asked for and their errors. Telegram still asking to wait after
// tg waited once is a limit on the bot, not on a language: the languages
// left are not sent into it, and count as failed.
func (b *Bot) publishLanguages(ctx context.Context, codes []string) (failed []string, wait time.Duration, err error) {
	var errs []error
	for i, code := range codes {
		if ctx.Err() != nil {
			return failed, wait, errors.Join(errs...)
		}
		lerr := b.publishLanguage(ctx, code)
		if lerr == nil {
			continue
		}
		failed = append(failed, code)
		errs = append(errs, fmt.Errorf("%s: %w", languageName(code), lerr))
		if tg.IsTooManyRequests(lerr) {
			failed = append(failed, codes[i+1:]...)
			return failed, tg.RetryAfter(lerr), errors.Join(errs...)
		}
	}
	return failed, wait, errors.Join(errs...)
}

// publishLanguage publishes one language's menus and texts, each of them
// whatever became of the one before.
func (b *Bot) publishLanguage(ctx context.Context, code string) error {
	lang := code
	if lang == "" {
		lang = i18n.DefaultLang
	}
	var errs []error
	private := []tg.BotCommand{{Command: "start", Description: i18n.T(lang, "cmd.start")}}
	if err := b.tg.SetMyCommandsForScope(ctx, private, &tg.BotCommandScope{Type: "all_private_chats", LanguageCode: code}); err != nil {
		errs = append(errs, fmt.Errorf("direct chat commands: %w", err))
	}
	group := []tg.BotCommand{
		{Command: "img", Description: i18n.T(lang, "cmd.img")},
		{Command: "edit", Description: i18n.T(lang, "cmd.edit")},
		{Command: "start", Description: i18n.T(lang, "cmd.start_group"), IsEphemeral: true},
	}
	if err := b.tg.SetMyCommandsForScope(ctx, group, &tg.BotCommandScope{Type: "all_group_chats", LanguageCode: code}); err != nil {
		errs = append(errs, fmt.Errorf("group commands: %w", err))
	}
	if err := b.setDescriptions(ctx, code, i18n.T(lang, "bot.description"), i18n.T(lang, "bot.short")); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// languageName is a language code as the log shows it.
func languageName(code string) string {
	if code == "" {
		return "none"
	}
	return code
}

func languageNames(codes []string) []string {
	names := make([]string, len(codes))
	for i, c := range codes {
		names[i] = languageName(c)
	}
	return names
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
	var errs []error
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
			errs = append(errs, fmt.Errorf("%s: %w", d.field, err))
		}
	}
	return errors.Join(errs...)
}
