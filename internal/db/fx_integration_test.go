// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/fx"
)

func fxStore(t *testing.T) *Store {
	t.Helper()
	store := testStore(t)
	reset := func() {
		if _, err := store.pool.Exec(context.Background(), `TRUNCATE framy.fx_rate`); err != nil {
			t.Fatal(err)
		}
	}
	reset()
	t.Cleanup(reset)
	return store
}

func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// nbuDay is the rows of one day as a sync saves them: the NBU's USD, EUR
// and PLN, and BYN from currency-api.
func nbuDay(d string, usd float64) []fx.Rate {
	day := date(d)
	return []fx.Rate{
		{Date: day, Code: fx.USD, UAHPerUnit: usd, Source: fx.NBU},
		{Date: day, Code: fx.EUR, UAHPerUnit: 50.9527, Source: fx.NBU},
		{Date: day, Code: fx.PLN, UAHPerUnit: 11.5669, Source: fx.NBU},
		{Date: day, Code: fx.BYN, UAHPerUnit: usd / 2.94785894, Source: fx.CurrencyAPI},
	}
}

func TestRatesDateNeedsEveryCode(t *testing.T) {
	store := fxStore(t)
	ctx := context.Background()
	if d, err := store.RatesDate(ctx); err != nil || !d.IsZero() {
		t.Fatalf("empty: %v, %v", d, err)
	}
	if err := store.SaveRates(ctx, nbuDay("2026-10-27", 45.5382)); err != nil {
		t.Fatal(err)
	}
	// The next day without BYN, as when currency-api is down.
	if err := store.SaveRates(ctx, nbuDay("2026-10-28", 45.6)[:3]); err != nil {
		t.Fatal(err)
	}
	if d, err := store.RatesDate(ctx); err != nil || !d.Equal(date("2026-10-27")) {
		t.Fatalf("rates date = %v, %v; want the last complete day", d, err)
	}
	if err := store.SaveRates(ctx, nbuDay("2026-10-28", 45.6)[3:]); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.RatesDate(ctx); !d.Equal(date("2026-10-28")) {
		t.Fatalf("rates date = %v after BYN came", d)
	}
}

func TestRateDaysAreTheCompleteDaysInTheRange(t *testing.T) {
	store := fxStore(t)
	ctx := context.Background()
	for _, rates := range [][]fx.Rate{
		nbuDay("2026-10-26", 45.5),
		nbuDay("2026-10-28", 45.6),
		nbuDay("2026-10-29", 45.7)[:3], // no BYN yet
		nbuDay("2026-11-02", 45.3),
	} {
		if err := store.SaveRates(ctx, rates); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.RateDays(ctx, date("2026-10-27"), date("2026-11-02"))
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{date("2026-10-28"), date("2026-11-02")}
	if len(got) != len(want) {
		t.Fatalf("days = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("days = %v, want %v", got, want)
		}
	}
	if got, err := store.RateDays(ctx, date("2026-10-30"), date("2026-11-01")); err != nil || len(got) != 0 {
		t.Fatalf("a range of holes = %v, %v", got, err)
	}
}

func TestTheNBUWinsOverCurrencyAPI(t *testing.T) {
	store := fxStore(t)
	ctx := context.Background()
	day := date("2026-10-27")
	standIn := []fx.Rate{{Date: day, Code: fx.USD, UAHPerUnit: 45.67, Source: fx.CurrencyAPI}}
	if err := store.SaveRates(ctx, standIn); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRates(ctx, nbuDay("2026-10-27", 45.5382)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRates(ctx, standIn); err != nil {
		t.Fatal(err)
	}
	var rate float64
	var source string
	if err := store.pool.QueryRow(ctx, `SELECT uah_per_unit::float8, source FROM fx_rate WHERE date = $1 AND code = 'USD'`, day).Scan(&rate, &source); err != nil {
		t.Fatal(err)
	}
	if rate != 45.5382 || source != fx.NBU {
		t.Fatalf("USD = %v from %s, want the NBU's", rate, source)
	}
}

func TestQuote(t *testing.T) {
	store := fxStore(t)
	ctx := context.Background()
	if _, err := store.Quote(ctx, fx.EUR, date("2026-10-27")); !errors.Is(err, fx.ErrNoRate) {
		t.Fatalf("no rates: %v", err)
	}
	if q, err := store.Quote(ctx, fx.USD, date("2026-10-27")); err != nil || q != fx.Dollar {
		t.Fatalf("USD = %+v, %v", q, err)
	}
	for _, d := range []struct {
		day string
		usd float64
	}{{"2026-10-26", 45.4}, {"2026-10-27", 45.5382}, {"2026-10-28", 45.6}} {
		if err := store.SaveRates(ctx, nbuDay(d.day, d.usd)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		code, day, rateDay string
		perUSD             float64
		source             string
	}{
		// Tomorrow's rate, saved after 15:30, waits for its day.
		{fx.UAH, "2026-10-27", "2026-10-27", 45.5382, fx.NBU},
		{fx.EUR, "2026-10-27", "2026-10-27", 45.5382 / 50.9527, fx.NBU},
		{fx.PLN, "2026-10-27", "2026-10-27", 45.5382 / 11.5669, fx.NBU},
		{fx.BYN, "2026-10-27", "2026-10-27", 2.94785894, fx.CurrencyAPI},
		{fx.UAH, "2026-10-28", "2026-10-28", 45.6, fx.NBU},
		// A day past the last saved takes the last.
		{fx.UAH, "2026-11-02", "2026-10-28", 45.6, fx.NBU},
		// A day before the first saved, as on a first start in the
		// afternoon, takes the first.
		{fx.EUR, "2026-10-01", "2026-10-26", 45.4 / 50.9527, fx.NBU},
	} {
		q, err := store.Quote(ctx, tc.code, date(tc.day))
		if err != nil {
			t.Fatalf("%s on %s: %v", tc.code, tc.day, err)
		}
		if q.Code != tc.code || !q.Date.Equal(date(tc.rateDay)) || math.Abs(q.PerUSD-tc.perUSD) > 1e-9 || q.Source != tc.source {
			t.Errorf("%s on %s = %+v, want %v per USD of %s from %s", tc.code, tc.day, q, tc.perUSD, tc.rateDay, tc.source)
		}
	}
	// A day without BYN is skipped for BYN only.
	if err := store.SaveRates(ctx, nbuDay("2026-10-29", 45.7)[:3]); err != nil {
		t.Fatal(err)
	}
	if q, _ := store.Quote(ctx, fx.BYN, date("2026-10-29")); !q.Date.Equal(date("2026-10-28")) {
		t.Errorf("BYN on a day without its row = %+v", q)
	}
	if q, _ := store.Quote(ctx, fx.EUR, date("2026-10-29")); !q.Date.Equal(date("2026-10-29")) {
		t.Errorf("EUR on that day = %+v", q)
	}
	if _, err := store.Quote(ctx, "RUB", date("2026-10-29")); err == nil {
		t.Error("an unknown currency was quoted")
	}
}

func TestCurrencyChoice(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const id = 4101
	if err := store.Touch(ctx, tg.User{ID: id, FirstName: "Olena", LanguageCode: "uk"}, nil); err != nil {
		t.Fatal(err)
	}
	if c, err := store.Currency(ctx, id); err != nil || c != "" {
		t.Fatalf("before a choice: %q, %v", c, err)
	}
	if err := store.SetCurrency(ctx, id, fx.PLN); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.Currency(ctx, id); c != fx.PLN {
		t.Fatalf("after the choice: %q", c)
	}
	if err := store.SetCurrency(ctx, id, "RUB"); err == nil {
		t.Fatal("an unknown currency was saved")
	}
	if err := store.SetCurrency(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.Currency(ctx, id); c != "" {
		t.Fatalf("after going back to the default: %q", c)
	}
	if err := store.SetCurrency(ctx, 4999, fx.EUR); err == nil {
		t.Fatal("a currency was saved for a person core does not know")
	}
}
