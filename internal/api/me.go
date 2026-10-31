// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/keys"
)

// meJSON is the answer of GET /api/me.
type meJSON struct {
	UserID int64 `json:"user_id"`
	// Name and PhotoURL are what Telegram gave at the person's last sign-in,
	// empty when it gave none. The photo is Telegram's link, not a copy.
	Name     string `json:"name"`
	PhotoURL string `json:"photo_url"`
	// Language is the one the site should speak: core's, else the browser's.
	// LanguageChosen says it is the person's own pick in framy; false is
	// "Automatic", even when another bot of the family has a pick.
	Language       string `json:"language"`
	LanguageChosen bool   `json:"language_chosen"`
	// Currency is the one prices are shown in, by the rules of the estimate;
	// CurrencyChosen says it is the person's own pick, not a guess from the
	// country or the language.
	Currency       string `json:"currency"`
	CurrencyChosen bool   `json:"currency_chosen"`
	// SignedInAt is the sign-in that gave the name and the photo.
	SignedInAt *time.Time `json:"signed_in_at,omitempty"`
	// Connections are the person's keys, by provider, with the last four
	// characters of each, what the provider last said of it and what framy's
	// runs on it cost. Never the key.
	Connections []connectionJSON `json:"connections"`
	// DefaultModels are the models the composer opens on, by kind; a kind
	// without one is absent. A model may since have left the catalog.
	DefaultModels defaultModelsJSON `json:"default_models"`
	// DeliverToTelegram: the site's results go to the person's DM, where
	// the bot may write.
	DeliverToTelegram bool `json:"deliver_to_telegram"`
	// Free is what is left of the free allowance; absent when framy runs
	// without one.
	Free *usageJSON `json:"free,omitempty"`
	// Storage is how many files framy keeps for the person and their bytes,
	// for the settings; absent when framy keeps no files.
	Storage *storageJSON `json:"storage,omitempty"`
}

// connectionJSON is a connection with what framy's runs on the key cost, by
// the provider's bill, since the key was saved: Spent in all and Spent30Days
// over the last 30 days, in the person's currency at today's rate. Runs
// outside framy on the same key are not in them.
type connectionJSON struct {
	keys.Connection
	Spent       moneyJSON `json:"spent"`
	Spent30Days moneyJSON `json:"spent_30d"`
}

type defaultModelsJSON struct {
	Image string `json:"image,omitempty"`
	Video string `json:"video,omitempty"`
}

type storageJSON struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// usageJSON is the person's use of the free allowance, by kind.
type usageJSON struct {
	Images meterJSON `json:"images"`
	Videos meterJSON `json:"videos"`
}

// meterJSON is one kind of run: Left of Limit in a rolling window, and when
// the next slot frees up while none is left.
type meterJSON struct {
	Used          int        `json:"used"`
	Limit         int        `json:"limit"`
	Left          int        `json:"left"`
	WindowSeconds int64      `json:"window_seconds"`
	FreesAt       *time.Time `json:"frees_at,omitempty"`
}

func meterOf(m free.Meter) meterJSON {
	out := meterJSON{Used: m.Used, Limit: m.Limit, Left: m.Left(), WindowSeconds: int64(m.Window.Seconds())}
	if !m.FreesAt.IsZero() {
		at := m.FreesAt.UTC()
		out.FreesAt = &at
	}
	return out
}

// me is GET /api/me: who is signed in, for the header of the site and the
// Mini App, with their connections and what is left of the free allowance,
// for the composer. Like every /api answer it goes out no-store
// (internal/web).
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	card, err := a.cfg.Store.Card(ctx, user)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	lang := a.language(ctx, r, user)
	chosen, err := a.cfg.Store.Currency(ctx, user)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	q, err := a.quote(r, user, lang)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	conns, err := a.connections(r, user)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	settings, err := a.cfg.Store.Settings(ctx, user)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	var spent map[string]db.Spent
	if len(conns) > 0 {
		if spent, err = a.cfg.Store.Spent(ctx, user); err != nil {
			a.internal(w, r, err)
			return
		}
	}
	out := meJSON{
		UserID: user, Name: card.Name, PhotoURL: card.PhotoURL, Language: lang, LanguageChosen: settings.LanguagePicked,
		Currency: q.Code, CurrencyChosen: chosen != "" && q.Code == chosen, Connections: make([]connectionJSON, 0, len(conns)),
		DefaultModels:     defaultModelsJSON{Image: settings.DefaultImage, Video: settings.DefaultVideo},
		DeliverToTelegram: settings.Deliver,
	}
	for _, c := range conns {
		sp := spent[c.Provider]
		out.Connections = append(out.Connections, connectionJSON{Connection: c,
			Spent: money(sp.USD, q.Code, q.PerUSD, lang), Spent30Days: money(sp.Last30DaysUSD, q.Code, q.PerUSD, lang)})
	}
	if !card.At.IsZero() {
		at := card.At.UTC()
		out.SignedInAt = &at
	}
	if a.cfg.Allowance != nil {
		u, err := a.cfg.Allowance.Usage(ctx, user)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		out.Free = &usageJSON{Images: meterOf(u.Images), Videos: meterOf(u.Videos)}
	}
	if a.cfg.Files != nil {
		u, err := a.cfg.Files.Usage(ctx, user)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		out.Storage = &storageJSON{Files: u.Files, Bytes: u.Bytes}
	}
	writeJSON(w, http.StatusOK, out)
}
