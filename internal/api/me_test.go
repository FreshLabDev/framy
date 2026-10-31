// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/keys"
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
		"language": "uk", "language_chosen": false, "currency": "EUR", "currency_chosen": true, "signed_in_at": "2026-10-27T09:30:00Z",
		"deliver_to_telegram": true,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	// No KEK file, no connection: an empty list, not a missing one. No
	// allowance in this rig, so nothing is said of it.
	if c, ok := m["connections"].([]any); !ok || len(c) != 0 {
		t.Errorf("connections = %v", m["connections"])
	}
	// The rig keeps files, so the settings learn how much.
	if st, ok := m["storage"].(map[string]any); !ok || st["files"] != float64(0) || st["bytes"] != float64(0) {
		t.Errorf("storage = %v", m["storage"])
	}
	// No default model yet: an empty object, the composer's own pick.
	if d, ok := m["default_models"].(map[string]any); !ok || len(d) != 0 {
		t.Errorf("default_models = %v", m["default_models"])
	}
	if len(m) != len(want)+3 {
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

func TestMeHasTheConnectionsAndTheAllowance(t *testing.T) {
	frees := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	allow := &fakeAllowance{usage: free.Usage{
		Images: free.Meter{Used: 2, Limit: 5, Window: 24 * time.Hour},
		Videos: free.Meter{Used: 1, Limit: 1, Window: 7 * 24 * time.Hour, FreesAt: frees},
	}}
	r := newRig(t, withAllowance(allow))
	r.conns.list = []keys.Connection{{ID: "c1", Provider: "openrouter", Last4: "a1b2", Status: keys.Valid}, {ID: "c2", Provider: "openai", Last4: "c3d4", Status: keys.Valid}}
	r.store.chosen[alice] = "UAH"
	r.store.spent[alice] = map[string]db.Spent{"openrouter": {USD: 1.25, Last30DaysUSD: 0.5}}

	m := jsonOf(t, r.do(alice, "GET", "/api/me", nil))
	conns, _ := m["connections"].([]any)
	if len(conns) != 2 {
		t.Fatalf("connections = %v", m["connections"])
	}
	c := conns[0].(map[string]any)
	if c["provider"] != "openrouter" || c["last4"] != "a1b2" || c["status"] != "valid" || c["id"] != "c1" {
		t.Errorf("connection = %v", c)
	}
	// What the runs on the key cost, in the person's currency; a key with no
	// run yet cost nothing.
	spent, month := c["spent"].(map[string]any), c["spent_30d"].(map[string]any)
	if spent["usd"] != 1.25 || spent["amount"] != 1.25*41.5 || spent["currency"] != "UAH" || month["usd"] != 0.5 || month["amount"] != 0.5*41.5 {
		t.Errorf("spent = %v, spent_30d = %v", spent, month)
	}
	if l, _ := spent["label"].(string); l == "" {
		t.Errorf("spent has no label: %v", spent)
	}
	if none := conns[1].(map[string]any)["spent"].(map[string]any); none["usd"] != 0.0 || none["currency"] != "UAH" {
		t.Errorf("a key with no run: %v", none)
	}
	if _, leaked := c["user_id"]; leaked {
		t.Errorf("the connection names its owner: %v", c)
	}
	f := m["free"].(map[string]any)
	images, videos := f["images"].(map[string]any), f["videos"].(map[string]any)
	if images["used"] != 2.0 || images["left"] != 3.0 || images["limit"] != 5.0 || images["window_seconds"] != 86400.0 || images["frees_at"] != nil {
		t.Errorf("images = %v", images)
	}
	if videos["left"] != 0.0 || videos["frees_at"] != "2026-11-04T08:00:00Z" {
		t.Errorf("videos = %v", videos)
	}

	// A broken read is an error, not an allowance that looks untouched.
	allow.usageErr = errors.New("connection reset")
	if resp := r.do(alice, "GET", "/api/me", nil); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("%d", resp.StatusCode)
	}
	allow.usageErr = nil
	r.conns.err = errors.New("connection reset")
	if resp := r.do(alice, "GET", "/api/me", nil); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("%d", resp.StatusCode)
	}
}
