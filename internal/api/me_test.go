// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/db"
)

func TestMeIsTheSignedInPerson(t *testing.T) {
	r := newRig(t)
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	r.store.cards[alice] = db.Card{Name: "Olesya Kovalenko", PhotoURL: "https://t.me/i/userpic/320/olesya.jpg", At: at}
	r.store.lang[alice], r.store.chosen[alice] = "uk", "EUR"

	resp := r.do(alice, "GET", "/api/me", nil)
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	want := map[string]any{
		"user_id": float64(alice), "name": "Olesya Kovalenko", "photo_url": "https://t.me/i/userpic/320/olesya.jpg",
		"language": "uk", "currency": "EUR", "currency_chosen": true, "signed_in_at": "2026-10-27T09:30:00Z",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("fields %v", m)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
}

// A person with no card yet (signed in before names were kept) still gets an
// answer, with empty fields, and a currency guessed from the language is not
// called chosen.
func TestMeWithoutACard(t *testing.T) {
	r := newRig(t)
	r.store.lang[bob] = "pl"
	resp := r.do(bob, "GET", "/api/me", nil)
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusOK || m["name"] != "" || m["photo_url"] != "" || m["currency"] != "PLN" || m["currency_chosen"] != false {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if _, ok := m["signed_in_at"]; ok {
		t.Errorf("signed_in_at without a card: %v", m)
	}
	// A pick that is not a currency of ours is no pick.
	r.store.chosen[bob] = "GBP"
	if m := jsonOf(t, r.do(bob, "GET", "/api/me", nil)); m["currency"] != "PLN" || m["currency_chosen"] != false {
		t.Errorf("%v", m)
	}
}

func TestMeNeedsASignIn(t *testing.T) {
	r := newRig(t)
	resp := r.do(0, "GET", "/api/me", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusUnauthorized || m["error"] != codeSignIn {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}

func TestMeWhenTheDatabaseFails(t *testing.T) {
	r := newRig(t)
	r.store.cardErr = errors.New("connection reset")
	resp := r.do(alice, "GET", "/api/me", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusInternalServerError || m["error"] != codeInternal {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}
