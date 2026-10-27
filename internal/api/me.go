// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"
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
}

// me is GET /api/me: who is signed in, for the header of the site and the
// Mini App. Like every /api answer it goes out no-store (internal/web).
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
	out := meJSON{
		UserID: user, Name: card.Name, PhotoURL: card.PhotoURL, Language: lang,
		Currency: q.Code, CurrencyChosen: chosen != "" && q.Code == chosen,
	}
	if !card.At.IsZero() {
		at := card.At.UTC()
		out.SignedInAt = &at
	}
	writeJSON(w, http.StatusOK, out)
}
