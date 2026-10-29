// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"

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
	Language string `json:"language"`
	// Currency is the one prices are shown in, by the rules of the estimate;
	// CurrencyChosen says it is the person's own pick, not a guess from the
	// country or the language.
	Currency       string `json:"currency"`
	CurrencyChosen bool   `json:"currency_chosen"`
	// SignedInAt is the sign-in that gave the name and the photo.
	SignedInAt *time.Time `json:"signed_in_at,omitempty"`
	// Connections are the person's keys, by provider, with the last four
	// characters of each and what the provider last said of it. Never the
	// key.
	Connections []keys.Connection `json:"connections"`
	// Free is what is left of the free allowance; absent when framy runs
	// without one.
	Free *usageJSON `json:"free,omitempty"`
	// Storage is how many files framy keeps for the person and their bytes,
	// for the settings; absent when framy keeps no files.
	Storage *storageJSON `json:"storage,omitempty"`
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
	if conns == nil {
		conns = []keys.Connection{}
	}
	out := meJSON{
		UserID: user, Name: card.Name, PhotoURL: card.PhotoURL, Language: lang,
		Currency: q.Code, CurrencyChosen: chosen != "" && q.Code == chosen, Connections: conns,
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
