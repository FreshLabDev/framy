// SPDX-License-Identifier: Apache-2.0
package fx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The NBU's answer of 27 October 2026, cut to a few rows.
const nbuAnswer = `[
{"r030":36,"txt":"Австралійський долар","rate":29.5172,"cc":"AUD","exchangedate":"27.10.2026"},
{"r030":840,"txt":"Долар США","rate":45.5382,"cc":"USD","exchangedate":"27.10.2026","special":"N"},
{"r030":978,"txt":"Євро","rate":50.9527,"cc":"EUR","exchangedate":"27.10.2026","special":null},
{"r030":985,"txt":"Злотий","rate":11.5669,"cc":"PLN","exchangedate":"27.10.2026","special":null},
{"r030":959,"txt":"Золото","rate":0,"cc":"XAU","exchangedate":"27.10.2026"}
]`

// currency-api's answer of 27 October 2026, cut.
const usdAnswer = `{"date":"2026-10-27","usd":{"uah":45.67066934,"eur":0.89509869,"pln":3.9416428,"byn":2.94785894,"usd":1}}`

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

func serve(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestNBU(t *testing.T) {
	c := &Client{NBUURL: serve(t, 200, nbuAnswer)}
	got, err := c.NBU(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Date.Equal(day("2026-10-27")) || got.UAH[USD] != 45.5382 || got.UAH[EUR] != 50.9527 || got.UAH[PLN] != 11.5669 {
		t.Fatalf("nbu = %+v", got)
	}
	if _, ok := got.UAH["XAU"]; ok {
		t.Fatal("a zero rate was kept")
	}
	for name, body := range map[string]string{
		"no PLN":        strings.Replace(nbuAnswer, `"cc":"PLN"`, `"cc":"PLX"`, 1),
		"two days":      strings.Replace(nbuAnswer, `"cc":"EUR","exchangedate":"27.10.2026"`, `"cc":"EUR","exchangedate":"28.10.2026"`, 1),
		"a future date": `[]`,
		"a broken date": strings.ReplaceAll(nbuAnswer, "27.10.2026", "2026-10-27"),
		"not json":      `<html>maintenance</html>`,
	} {
		c := &Client{NBUURL: serve(t, 200, body)}
		if _, err := c.NBU(context.Background()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := (&Client{NBUURL: serve(t, 503, nbuAnswer)}).NBU(context.Background()); err == nil {
		t.Error("a 503 was accepted")
	}
}

func TestCurrencyAPITakesTheNewestMirror(t *testing.T) {
	c := &Client{CurrencyAPIURLs: []string{serve(t, 404, ""), serve(t, 200, usdAnswer)}}
	got, err := c.CurrencyAPI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Date.Equal(day("2026-10-27")) || got.PerUSD[BYN] != 2.94785894 || got.PerUSD[UAH] != 45.67066934 {
		t.Fatalf("currency-api = %+v", got)
	}
	// jsDelivr's cache a day behind Cloudflare Pages.
	stale := strings.NewReplacer(`"2026-10-27"`, `"2026-10-26"`, `"byn":2.94785894`, `"byn":2.92955547`).Replace(usdAnswer)
	for _, order := range [][]string{{stale, usdAnswer}, {usdAnswer, stale}} {
		c := &Client{CurrencyAPIURLs: []string{serve(t, 200, order[0]), serve(t, 200, order[1])}}
		got, err := c.CurrencyAPI(context.Background())
		if err != nil || !got.Date.Equal(day("2026-10-27")) || got.PerUSD[BYN] != 2.94785894 {
			t.Fatalf("the newer day was not taken: %+v, %v", got, err)
		}
	}
	noBYN := strings.Replace(usdAnswer, `"byn"`, `"bif"`, 1)
	c = &Client{CurrencyAPIURLs: []string{serve(t, 200, noBYN), serve(t, 500, "")}}
	if _, err := c.CurrencyAPI(context.Background()); err == nil {
		t.Fatal("currency-api without BYN was accepted")
	}
}

func TestCross(t *testing.T) {
	nbu := NBUDay{Date: day("2026-10-27"), UAH: map[string]float64{USD: 45.5382, EUR: 50.9527, PLN: 11.5669}}
	usd := USDDay{Date: day("2026-10-26"), PerUSD: map[string]float64{UAH: 45.67066934, EUR: 0.89509869, PLN: 3.9416428, BYN: 2.94785894}}
	rates := FromNBU(nbu, &usd)
	by := map[string]Rate{}
	for _, r := range rates {
		if !r.Date.Equal(nbu.Date) {
			t.Fatalf("%s is for %s, not the NBU's day", r.Code, r.Date)
		}
		by[r.Code] = r
	}
	for code, want := range map[string]float64{
		USD: 1,
		UAH: 45.5382,
		EUR: 45.5382 / 50.9527,
		PLN: 45.5382 / 11.5669,
		// BYN is anchored on the NBU's USD, so a price in BYN is the USD
		// price times currency-api's own rate.
		BYN: 2.94785894,
	} {
		q := Cross(code, by[USD], by[code])
		if !near(q.PerUSD, want) {
			t.Errorf("%s per USD = %v, want %v", code, q.PerUSD, want)
		}
		if code != USD && !q.Date.Equal(nbu.Date) {
			t.Errorf("%s quote is for %s", code, q.Date)
		}
	}
	if q := Cross(BYN, by[USD], by[BYN]); q.Source != CurrencyAPI {
		t.Errorf("BYN source = %s", q.Source)
	}
	if q := Cross(EUR, by[USD], by[EUR]); q.Source != NBU {
		t.Errorf("EUR source = %s", q.Source)
	}
	if got := FromNBU(nbu, nil); len(got) != 3 || slices.ContainsFunc(got, func(r Rate) bool { return r.Code == BYN }) {
		t.Errorf("without currency-api: %+v", got)
	}

	alone := FromCurrencyAPI(usd)
	by = map[string]Rate{}
	for _, r := range alone {
		if r.Source != CurrencyAPI || !r.Date.Equal(usd.Date) {
			t.Fatalf("fallback row %+v", r)
		}
		by[r.Code] = r
	}
	for _, code := range []string{UAH, EUR, PLN, BYN} {
		if q := Cross(code, by[USD], by[code]); !near(q.PerUSD, usd.PerUSD[code]) {
			t.Errorf("fallback %s per USD = %v, want %v", code, q.PerUSD, usd.PerUSD[code])
		}
	}
}

func TestPricesInEveryCurrency(t *testing.T) {
	d := day("2026-10-27")
	usdRow := Rate{Date: d, Code: USD, UAHPerUnit: 45.5382, Source: NBU}
	rows := map[string]Rate{
		EUR: {Date: d, Code: EUR, UAHPerUnit: 50.9527, Source: NBU},
		PLN: {Date: d, Code: PLN, UAHPerUnit: 11.5669, Source: NBU},
		BYN: {Date: d, Code: BYN, UAHPerUnit: 45.5382 / 2.94785894, Source: CurrencyAPI},
	}
	// MAI Image 2.6 Flash, the free model: $0.0195 an image.
	const usd = 0.0195
	for _, tc := range []struct{ code, lang, want string }{
		{UAH, "uk", "0,89 ₴"},
		{BYN, "be", "0,058 Br"},
		{PLN, "pl", "0,077 zł"},
		{EUR, "de", "0,018 €"},
		{EUR, "en", "€0.018"},
		{PLN, "en", "0.077 zł"},
		{USD, "en", "$0.02"},
	} {
		q := Cross(tc.code, usdRow, rows[tc.code])
		if got := Format(q.Convert(usd), tc.code, tc.lang); got != tc.want {
			t.Errorf("%s in %s = %q, want %q", tc.code, tc.lang, got, tc.want)
		}
		if tc.code != USD && !q.Date.Equal(d) {
			t.Errorf("%s has no rate date", tc.code)
		}
	}
}

func TestFormat(t *testing.T) {
	for _, tc := range []struct {
		amount     float64
		code, lang string
		want       string
	}{
		{0.0195, USD, "en", "$0.02"},
		{0.009, USD, "en", "$0.009"},
		{0.07, USD, "en", "$0.07"},
		{0.56, USD, "en", "$0.56"},
		{0.00012, USD, "en", "$0.0002"},
		{0.0000001, USD, "en", "$0.0001"},
		{12, USD, "en", "$12.00"},
		{1.231, EUR, "fr", "1,24 €"},
		{18.004, UAH, "uk-UA", "18,01 ₴"},
		{0.5, UAH, "ar", "₴0.50"},
		{0, USD, "en", "$0"},
		{math.NaN(), USD, "en", "$0"},
	} {
		if got := Format(tc.amount, tc.code, tc.lang); got != tc.want {
			t.Errorf("Format(%v, %s, %s) = %q, want %q", tc.amount, tc.code, tc.lang, got, tc.want)
		}
	}
}

func TestFormatCodePutsTheCodeAfterTheNumber(t *testing.T) {
	for _, tc := range []struct {
		amount     float64
		code, lang string
		want       string
	}{
		{18.004, UAH, "uk", "18,01 UAH"},
		{0.5, UAH, "en", "0.50 UAH"},
		{2.049, "RUB", "ru", "2,05 RUB"},
	} {
		if got := FormatCode(tc.amount, tc.code, tc.lang); got != tc.want {
			t.Errorf("FormatCode(%v, %s, %s) = %q, want %q", tc.amount, tc.code, tc.lang, got, tc.want)
		}
	}
}

func TestFormatNeverRoundsDown(t *testing.T) {
	for i := 1; i < 20000; i++ {
		amount := float64(i) * 0.00137
		s := strings.TrimPrefix(Format(amount, USD, "en"), "$")
		shown, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		if shown < amount-1e-12 {
			t.Fatalf("%v shown as %s", amount, s)
		}
	}
}

func TestDefault(t *testing.T) {
	for _, tc := range []struct{ chosen, country, lang, want string }{
		{"PLN", "UA", "uk", PLN},
		{"", "UA", "en", UAH},
		{"", "by", "", BYN},
		{"", "PL", "", PLN},
		{"", "DE", "de", EUR},
		{"", "BG", "", EUR},
		{"", "ME", "", EUR},
		{"", "US", "uk", USD},
		{"", "GB", "pl", USD},
		{"", "XX", "uk", UAH},
		{"", "T1", "be", BYN},
		{"", "", "uk", UAH},
		{"", "", "be", BYN},
		{"", "", "pl", PLN},
		{"", "", "ru", USD},
		{"", "", "de", USD},
		{"", "", "", USD},
		{"RUB", "", "uk", UAH},
	} {
		if got := Default(tc.chosen, tc.country, tc.lang); got != tc.want {
			t.Errorf("Default(%q, %q, %q) = %s, want %s", tc.chosen, tc.country, tc.lang, got, tc.want)
		}
	}
}

func kyiv(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, Kyiv)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWant(t *testing.T) {
	for _, tc := range []struct{ now, want, next string }{
		{"2026-10-27 00:00", "2026-10-27", "2026-10-27 15:35"},
		{"2026-10-27 12:00", "2026-10-27", "2026-10-27 15:35"},
		{"2026-10-27 15:34", "2026-10-27", "2026-10-27 15:35"},
		{"2026-10-27 15:35", "2026-10-28", "2026-10-28 00:00"},
		{"2026-10-27 23:59", "2026-10-28", "2026-10-28 00:00"},
		// Kyiv is UTC+2 in winter: 23:00 UTC is already the next day there.
		{"2026-12-31 23:30", "2027-01-01", "2027-01-01 00:00"},
	} {
		now := kyiv(tc.now)
		if got := Want(now); !got.Equal(day(tc.want)) {
			t.Errorf("Want(%s) = %s, want %s", tc.now, got.Format(time.DateOnly), tc.want)
		}
		if got := next(now); !got.Equal(kyiv(tc.next)) {
			t.Errorf("next(%s) = %s, want %s", tc.now, got.In(Kyiv).Format("2006-01-02 15:04"), tc.next)
		}
	}
	if got := Day(time.Date(2026, 10, 27, 22, 30, 0, 0, time.UTC)); !got.Equal(day("2026-10-28")) {
		t.Errorf("Day of 22:30 UTC = %s, want Kyiv's 28th", got)
	}
}

// memStore keeps rates as the database does: the NBU's row wins over
// currency-api's for a day and code.
type memStore struct {
	rows  map[string]Rate
	saves int
	fail  error
}

func (m *memStore) SaveRates(_ context.Context, rates []Rate) error {
	if m.fail != nil {
		return m.fail
	}
	m.saves++
	if m.rows == nil {
		m.rows = map[string]Rate{}
	}
	for _, r := range rates {
		k := r.Date.Format(time.DateOnly) + r.Code
		if old, ok := m.rows[k]; ok && old.Source == NBU && r.Source != NBU {
			continue
		}
		m.rows[k] = r
	}
	return nil
}

func (m *memStore) RatesDate(context.Context) (time.Time, error) {
	count := map[time.Time]int{}
	for _, r := range m.rows {
		count[r.Date]++
	}
	var latest time.Time
	for d, n := range count {
		if n == len(Stored) && d.After(latest) {
			latest = d
		}
	}
	return latest, nil
}

type fakeSource struct {
	nbu      NBUDay
	usd      USDDay
	nbuErr   error
	usdErr   error
	requests int
}

func (f *fakeSource) NBU(context.Context) (NBUDay, error) {
	f.requests++
	return f.nbu, f.nbuErr
}

func (f *fakeSource) CurrencyAPI(context.Context) (USDDay, error) {
	f.requests++
	return f.usd, f.usdErr
}

func source(nbuDay, usdDay string) *fakeSource {
	return &fakeSource{
		nbu: NBUDay{Date: day(nbuDay), UAH: map[string]float64{USD: 45.5382, EUR: 50.9527, PLN: 11.5669}},
		usd: USDDay{Date: day(usdDay), PerUSD: map[string]float64{UAH: 45.67066934, EUR: 0.89509869, PLN: 3.9416428, BYN: 2.94785894}},
	}
}

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestStep(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	src := source("2026-10-28", "2026-10-27")
	store := &memStore{}
	now := kyiv("2026-10-27 15:40")
	s := &Syncer{Source: src, Store: store, Log: logTo(&logs), Now: func() time.Time { return now }}

	// Behind: the rates are fetched, and the next step is at midnight.
	if wait := s.Step(ctx); wait != kyiv("2026-10-28 00:00").Sub(now) {
		t.Fatalf("wait after a sync = %s", wait)
	}
	if src.requests != 2 || store.saves != 1 {
		t.Fatalf("requests %d, saves %d", src.requests, store.saves)
	}
	// Up to date: a restart asks no one.
	if wait := s.Step(ctx); wait != kyiv("2026-10-28 00:00").Sub(now) || src.requests != 2 {
		t.Fatalf("an up-to-date step asked again: wait %s, requests %d", wait, src.requests)
	}

	// The NBU has not published the next day yet: ask again in an hour.
	now = kyiv("2026-10-28 15:40")
	src.nbu.Date = day("2026-10-28")
	if wait := s.Step(ctx); wait != time.Hour {
		t.Fatalf("wait while the NBU is behind = %s", wait)
	}

	// Both sources down: retry soon, keep what is saved.
	src.nbuErr, src.usdErr = errors.New("nbu down"), errors.New("cdn down")
	before := len(store.rows)
	if wait := s.Step(ctx); wait != 15*time.Minute || len(store.rows) != before {
		t.Fatalf("both down: wait %s, rows %d → %d", wait, before, len(store.rows))
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatal("both sources down was not logged as an error")
	}

	// Close to midnight the retry does not overshoot the next slot.
	now = kyiv("2026-10-28 23:55")
	if wait := s.Step(ctx); wait != 5*time.Minute {
		t.Fatalf("retry before midnight = %s", wait)
	}
}

func TestNBUFailureFallsBackWithoutAnError(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	src := source("2026-10-27", "2026-10-27")
	src.nbuErr = errors.New("status 503")
	store := &memStore{}
	s := &Syncer{Source: src, Store: store, Log: logTo(&logs), Now: func() time.Time { return kyiv("2026-10-27 09:00") }}
	got, err := s.SyncOnce(ctx)
	if err != nil {
		t.Fatalf("fallback returned an error: %v", err)
	}
	if !got.Equal(day("2026-10-27")) {
		t.Fatalf("rates date = %s", got)
	}
	for _, r := range store.rows {
		if r.Source != CurrencyAPI {
			t.Fatalf("row %+v", r)
		}
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "nbu failed; rates from currency-api") {
		t.Fatalf("logs: %s", logs.String())
	}

	// The NBU back: its rows replace currency-api's for the same day.
	src.nbuErr = nil
	if _, err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{USD, EUR, PLN} {
		if r := store.rows["2026-10-27"+code]; r.Source != NBU {
			t.Fatalf("%s still from %s", code, r.Source)
		}
	}
	// A later currency-api stand-in does not replace them.
	src.nbuErr = errors.New("down again")
	if _, err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r := store.rows["2026-10-27"+USD]; r.Source != NBU || r.UAHPerUnit != 45.5382 {
		t.Fatalf("USD replaced by %+v", r)
	}
}

func TestCurrencyAPIDownLeavesTheDayIncomplete(t *testing.T) {
	src := source("2026-10-28", "2026-10-27")
	src.usdErr = errors.New("cdn down")
	store := &memStore{}
	s := &Syncer{Source: src, Store: store, Log: logTo(&bytes.Buffer{}), Now: func() time.Time { return kyiv("2026-10-27 16:00") }}
	if wait := s.Step(context.Background()); wait != 15*time.Minute {
		t.Fatalf("wait = %s", wait)
	}
	if got, _ := store.RatesDate(context.Background()); !got.IsZero() {
		t.Fatalf("a day without BYN counted as complete: %s", got)
	}
	if len(store.rows) != 3 {
		t.Fatalf("the NBU's rows were not kept: %d", len(store.rows))
	}
}
