// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"errors"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
)

// homeReadTimeout bounds, all together, what the home panel reads about the
// person. /start must answer within seconds, so a read that has not come back
// by then is left out of the panel.
const homeReadTimeout = 3 * time.Second

// account is what the home panel of a direct chat says about the person, read
// when the panel is drawn. A part that could not be read has its flag unset
// and its line is left out; nothing is guessed. The default models are not
// here: nothing stores them yet.
type account struct {
	// appURL is where "Open framy" goes; empty: no button.
	appURL string

	usage    free.Usage
	hasUsage bool

	connections    []keys.Connection
	hasConnections bool

	// currency is the code prices are shown in; empty: not known.
	currency string
}

// readAccount reads the person's allowance, connections and currency. They
// share one short deadline, and a read that fails is logged and left out, so
// the panel answers whatever the database is doing. The bot knows no country
// (nothing carries one), so the currency is the person's choice, else the
// language's, else USD.
func (b *Bot) readAccount(ctx context.Context, userID int64, lang string) account {
	a := account{appURL: b.appURL}
	ctx, cancel := context.WithTimeout(ctx, b.readTimeout)
	defer cancel()
	if b.allowance != nil {
		if usage, err := b.allowance.Usage(ctx, userID); err != nil {
			b.log.Warn("home panel: the free allowance was not read", "user", userID, "error", err)
		} else {
			a.usage, a.hasUsage = usage, true
		}
	}
	if b.connections != nil {
		list, err := b.connections.List(ctx, userID)
		switch {
		case errors.Is(err, keys.ErrDisabled):
			// framy keeps no keys, so none is connected.
			a.hasConnections = true
		case err != nil:
			b.log.Warn("home panel: the connections were not read", "user", userID, "error", err)
		default:
			a.connections, a.hasConnections = list, true
		}
	}
	if chosen, err := b.store.Currency(ctx, userID); err != nil {
		b.log.Warn("home panel: the currency was not read", "user", userID, "error", err)
	} else {
		a.currency = fx.Default(chosen, "", lang)
	}
	return a
}

// fact is a row of a quote: what it is, then its value, as in the About card.
func fact(label, value string) string { return label + " · " + value }

// accountLines are the rows of the home panel that belong to the person.
//
// The free windows are written into the strings, as in the bot's description:
// "24 hours" and "7 days" are translated, not computed, and a test holds them
// to free.ImageWindow and free.VideoWindow.
func (v view) accountLines() []string {
	a := v.account
	var lines []string
	if a.hasUsage {
		left := func(key string, m free.Meter) string {
			return v.t(key, "left", strconv.Itoa(m.Left()), "limit", strconv.Itoa(m.Limit))
		}
		lines = append(lines,
			fact(v.t("home.free_images"), left("home.free_images_left", a.usage.Images)),
			fact(v.t("home.free_videos"), left("home.free_videos_left", a.usage.Videos)),
		)
	}
	if a.hasConnections {
		lines = append(lines, fact(v.t("home.keys"), v.connectionList(a.connections)))
	}
	lines = append(lines, fact(v.t("lang.title"), i18n.NameOf(v.lang)))
	if a.currency != "" {
		lines = append(lines, fact(v.t("home.currency"), a.currency))
	}
	return lines
}

// connectionList names the providers the person has connected, in the order of
// keys.Providers. A status is added only where it says something is wrong:
// valid and verified are what a connection is expected to be. Nothing of a
// key is shown, not even its last characters.
func (v view) connectionList(list []keys.Connection) string {
	if len(list) == 0 {
		return v.t("home.keys_none")
	}
	rank := func(provider string) int {
		if i := slices.Index(keys.Providers, provider); i >= 0 {
			return i
		}
		return len(keys.Providers)
	}
	list = slices.Clone(list)
	slices.SortStableFunc(list, func(a, b keys.Connection) int { return rank(a.Provider) - rank(b.Provider) })
	names := make([]string, len(list))
	for i, c := range list {
		names[i] = html.EscapeString(connectionName(c.Provider))
		if word := v.statusWord(c.Status); word != "" {
			names[i] += " (" + word + ")"
		}
	}
	return strings.Join(names, ", ")
}

// connectionName is a brand name, the same in every language.
func connectionName(provider string) string {
	switch provider {
	case catalog.OpenRouter:
		return "OpenRouter"
	case catalog.OpenAI:
		return "OpenAI"
	case catalog.Gemini:
		return "Gemini"
	}
	return provider
}

func (v view) statusWord(s keys.Status) string {
	switch s {
	case keys.Invalid:
		return v.t("home.status_invalid")
	case keys.NeedsBilling:
		return v.t("home.status_billing")
	case keys.NeedsVerification:
		return v.t("home.status_verification")
	case keys.NoCredits:
		return v.t("home.status_credits")
	}
	return ""
}

// openButton opens the site as a Mini App. Telegram accepts a web_app button
// only in a direct chat, which is why a group's panel has none. It is the one
// Primary button of the panel (R4): the site is where framy makes things, and
// it is the only button that leads there.
func (v view) openButton(appURL string) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: v.t("btn.open"), WebApp: &tg.WebAppInfo{URL: appURL}, Style: tg.StylePrimary}
}
