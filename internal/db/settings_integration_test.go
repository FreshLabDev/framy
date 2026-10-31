// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
)

func TestTheSettingsAreKept(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	settings := func(user int64) Settings {
		t.Helper()
		st, err := store.Settings(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	// Nobody changed anything: the DM is on, the rest is framy's default.
	if st := settings(4601); st != (Settings{Deliver: true}) {
		t.Fatalf("defaults = %+v", st)
	}
	if err := store.Touch(ctx, tg.User{ID: 4601, FirstName: "Ann", LanguageCode: "uk"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrency(ctx, 4601, "EUR"); err != nil {
		t.Fatal(err)
	}

	// The language framy picks is said to be picked, and the currency beside
	// it stays.
	if err := store.SetUserLang(ctx, 4601, "pl"); err != nil {
		t.Fatal(err)
	}
	if st := settings(4601); !st.LanguagePicked {
		t.Errorf("after the pick: %+v", st)
	}
	if lang, err := store.UserLang(ctx, 4601); err != nil || lang != "pl" {
		t.Errorf("language %q %v", lang, err)
	}
	if code, err := store.Currency(ctx, 4601); err != nil || code != "EUR" {
		t.Errorf("currency %q %v", code, err)
	}
	if err := store.ClearUserLang(ctx, 4601); err != nil {
		t.Fatal(err)
	}
	if st := settings(4601); st.LanguagePicked {
		t.Errorf("after the withdrawal: %+v", st)
	}

	for _, set := range []struct {
		kind catalog.Kind
		id   string
	}{{catalog.Image, "test/chat"}, {catalog.Video, "test/video"}, {catalog.Image, ""}} {
		if err := store.SetDefaultModel(ctx, 4601, set.kind, set.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDelivery(ctx, 4601, false); err != nil {
		t.Fatal(err)
	}
	if st := settings(4601); st != (Settings{DefaultVideo: "test/video"}) {
		t.Errorf("settings = %+v", st)
	}
	if err := store.SetDefaultModel(ctx, 4601, "audio", "x"); err == nil {
		t.Error("a default for a kind framy has none of")
	}
	// The settings are the person's own.
	if st := settings(4602); st != (Settings{Deliver: true}) {
		t.Errorf("another person: %+v", st)
	}
}

// With the DM off, a site result is listed as not to be sent, and ends as
// off.
func TestASiteResultWithTheDMOff(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	job := siteJobOf(t, store, 4603, "site", time.Minute, false)
	if err := store.SetDelivery(ctx, 4603, false); err != nil {
		t.Fatal(err)
	}
	got, err := store.SiteResults(ctx, time.Now().Add(-time.Hour), 20)
	if err != nil || len(got) != 1 || got[0].JobID != job || got[0].Deliver {
		t.Fatalf("site results = %+v %v", got, err)
	}
	if ok, err := store.ClaimSiteResult(ctx, job); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := store.EndSiteResult(ctx, job, DeliveryOff); err != nil {
		t.Fatal(err)
	}
	// Turned on again, the result is not sent after all.
	if err := store.SetDelivery(ctx, 4603, true); err != nil {
		t.Fatal(err)
	}
	if got, err := store.SiteResults(ctx, time.Now().Add(-time.Hour), 20); err != nil || len(got) != 0 {
		t.Errorf("after the DM is on again: %+v %v", got, err)
	}
}

// What the runs on a key cost counts the person's paid runs on that
// provider since the key was saved, whose cost the provider gave.
func TestSpentIsByKey(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const user = 4604
	run := func(provider string, free bool, actual *float64, ago time.Duration) {
		t.Helper()
		job := chatJob(t, store, user, "spent:"+provider+":"+ago.String()+":"+time.Now().Format(time.RFC3339Nano))
		_, err := store.pool.Exec(ctx, `
			UPDATE job SET provider = $2, free = $3, actual_usd = $4, status = 'done', finished_at = now(),
			       created_at = now() - make_interval(secs => $5)
			WHERE id = $1`, job, provider, free, actual, ago.Seconds())
		if err != nil {
			t.Fatal(err)
		}
	}
	usd := func(v float64) *float64 { return &v }
	day := 24 * time.Hour

	if sp, err := store.Spent(ctx, user); err != nil || len(sp) != 0 {
		t.Fatalf("no key: %v %v", sp, err)
	}
	if err := store.Touch(ctx, tg.User{ID: user}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connection (id, user_id, provider, ciphertext, wrapped_dek, kek_version, last4, status, created_at)
		VALUES (gen_random_uuid(), $1, 'openrouter', '\x00', '\x00', 1, 'a1b2', 'valid', now() - interval '40 days')`, user); err != nil {
		t.Fatal(err)
	}
	run("openrouter", false, usd(0.3), 35*day) // on the key, past 30 days
	run("openrouter", false, usd(0.125), day)  // on the key, this month
	run("openrouter", false, usd(0.075), time.Hour)
	run("openrouter", true, usd(0.5), day)   // free: framy's key
	run("openrouter", false, nil, day)       // no bill yet
	run("openrouter", false, usd(1), 45*day) // before this key
	run("openai", false, usd(2), day)        // no key of that provider

	sp, err := store.Spent(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	got := sp["openrouter"]
	if len(sp) != 1 || math.Abs(got.USD-0.5) > 1e-9 || math.Abs(got.Last30DaysUSD-0.2) > 1e-9 {
		t.Errorf("spent = %+v", sp)
	}
	if other, err := store.Spent(ctx, 4605); err != nil || len(other) != 0 {
		t.Errorf("another person: %v %v", other, err)
	}
}
