// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
)

// The way to the settings (docs/design.md, `/img` step 3). A refusal that
// a key of the person's would lift, and the error card of a run on a key
// the provider refused, carry a button to where that key is connected. In
// framy's direct chat it opens the Mini App at the key, signed in. Telegram
// takes a Mini App button nowhere else, and a link to the site would ask the
// person to sign in again in a browser, so in a group and in a guest's chat
// the button opens framy's direct chat with /start settings_<provider>,
// whose panel opens the Mini App at the key. A key out of credits is topped
// up at the provider, so its card links there.

// startSettings starts the /start parameter of that link; the provider
// follows. Messages already sent carry it, so internal/bot reads it for
// good.
const startSettings = "settings_"

// refusal is why a command makes no run: the text the person reads and, when
// a key would let it go on, that key's provider.
type refusal struct {
	text string
	// connect is the provider whose key runs it; empty when none would.
	connect string
}

// keyButton leads to the settings at provider's key: a Mini App button
// named label in framy's direct chat, a link to that chat named "Open
// framy" elsewhere. ok is false when framy has neither a Mini App nor its
// own name to link to.
func (f *Flow) keyButton(lang string, direct bool, provider, label string) (tg.InlineKeyboardButton, bool) {
	switch {
	case direct && f.cfg.AppURL != "":
		return tg.InlineKeyboardButton{Text: label, WebApp: &tg.WebAppInfo{URL: keys.SettingsURL(f.cfg.AppURL, provider)}, Style: tg.StylePrimary}, true
	case f.cfg.Username != "":
		return tg.InlineKeyboardButton{Text: t(lang, "btn.open"), URL: "https://t.me/" + f.cfg.Username + "?start=" + startSettings + provider, Style: tg.StylePrimary}, true
	}
	return tg.InlineKeyboardButton{}, false
}

// refusalKeyboard is what goes under a refusal in chat: the way to connect
// the key that lifts it, if one would.
func (f *Flow) refusalKeyboard(lang string, direct bool, r refusal) *tg.InlineKeyboardMarkup {
	if r.connect == "" {
		return nil
	}
	b, ok := f.keyButton(lang, direct, r.connect, t(lang, "web.go.connect", "provider", catalog.ProviderName(r.connect)))
	if !ok {
		return nil
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{b}}}
}

// keyFix is what helps a run that failed on the person's own key: the
// provider refused it, and it is replaced in the settings, or it is out of
// credits, and it is topped up at the provider. A free run failed on framy's
// key, which is none of the person's business. The card of a run from the
// chat has its top-up already, above Try again (failureKeyboard); a guest's
// card and a site run's get it here.
func (f *Flow) keyFix(run db.ChatRun) []tg.InlineKeyboardButton {
	if run.Free {
		return nil
	}
	provider := runProvider(run)
	switch jobs.Class(run.ErrorClass) {
	case jobs.Money:
		if fromChat(run) && !guest(run) {
			return nil
		}
		if url := billing[provider]; url != "" {
			return []tg.InlineKeyboardButton{{Text: t(run.Lang, "web.act.topup", "provider", catalog.ProviderName(provider)), URL: url, Style: tg.StylePrimary}}
		}
	case jobs.Key:
		direct := !guest(run) && run.ChatID > 0
		if b, ok := f.keyButton(run.Lang, direct, provider, t(run.Lang, "web.go.rekey", "provider", catalog.ProviderName(provider))); ok {
			return []tg.InlineKeyboardButton{b}
		}
	}
	return nil
}

// withRow is kb with row under it.
func withRow(kb *tg.InlineKeyboardMarkup, row []tg.InlineKeyboardButton) *tg.InlineKeyboardMarkup {
	if len(row) == 0 {
		return kb
	}
	if kb == nil {
		return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{row}}
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: append(append([][]tg.InlineKeyboardButton{}, kb.InlineKeyboard...), row)}
}
