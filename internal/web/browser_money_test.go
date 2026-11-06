// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/FreshLabDev/framy/internal/fx"
)

// The site writes a few amounts itself: the dollars of the hint, and the sum of
// several runs, in the person's currency, from the labels the server wrote.
// Go's fx.Format is the rule (it never rounds down); these tests hold price.js
// to it, string for string.

func TestBrowserDollarsMatchGo(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, priceBundle(t)+"; return true;")

	// The sweep of fx's own test, the edges of the decimals, and what no price is.
	amounts := []float64{0, -1, -0.0001, 1e-12, 1e-9, 1e-5, 0.00005, 0.00009999, 0.0001, 0.0009, 0.001, 0.00999, 0.01, 0.07, 0.0701, 0.1, 0.12, 0.5, 0.99, 0.9999, 0.99991, 1, 1.004, 1.005, 1.5, 9.999, 10, 12.345, 99.995, 1234.5678, 1e6,
		math.NaN(), math.Inf(1), math.Inf(-1)}
	for i := 1; i < 20000; i += 7 {
		amounts = append(amounts, float64(i)*0.00137)
	}
	for e := -6; e <= 6; e++ {
		p := math.Pow10(e)
		amounts = append(amounts, p, math.Nextafter(p, 0), math.Nextafter(p, 1), 2*p, 5*p, 9.99*p)
	}
	rnd := rand.New(rand.NewSource(7))
	for i := 0; i < 3000; i++ {
		amounts = append(amounts, math.Exp(rnd.Float64()*14-9))
	}
	// JSON has no NaN or infinity, so the page gets them as strings.
	in := make([]any, len(amounts))
	want := make([]string, len(amounts))
	for i, a := range amounts {
		switch {
		case math.IsNaN(a):
			in[i] = "NaN"
		case math.IsInf(a, 1):
			in[i] = "Infinity"
		case math.IsInf(a, -1):
			in[i] = "-Infinity"
		default:
			in[i] = a
		}
		want[i] = fx.Format(a, fx.USD, "en")
	}
	body, _ := json.Marshal(in)
	got := js[[]string](t, ctx, fmt.Sprintf(`const P = window.__price;
	return %s.map((a) => P.usd(typeof a === "string" ? Number(a) : a));`, body))
	bad := 0
	for i := range amounts {
		if got[i] != want[i] {
			if bad++; bad <= 12 {
				t.Errorf("%v: the page writes %s, Go %s", amounts[i], got[i], want[i])
			}
		}
	}
	if bad > 12 {
		t.Errorf("... and %d more", bad-12)
	}
}

// est is the part of an estimate that sumOf reads.
type est struct {
	Kind      string   `json:"kind"`
	USD       float64  `json:"usd"`
	MaxUSD    *float64 `json:"max_usd,omitempty"`
	Currency  string   `json:"currency"`
	Amount    *float64 `json:"amount,omitempty"`
	MaxAmount *float64 `json:"max_amount,omitempty"`
	Label     string   `json:"label"`
}

func TestBrowserSumMatchesGo(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, priceBundle(t)+"; return true;")

	rate := map[string]float64{fx.USD: 1, fx.UAH: 41.7, fx.EUR: 0.86, fx.PLN: 3.66, fx.BYN: 3.05}
	langs := []string{"en", "ru", "uk", "pl", "de", "fr", "tr", "be"}
	kinds := []string{"exact", "about", "up_to", "from"}
	rank := map[string]int{"exact": 0, "about": 1, "up_to": 2, "from": 3}
	rnd := rand.New(rand.NewSource(11))
	type tc struct {
		Es   []est `json:"es"`
		Want struct {
			Label string  `json:"label"`
			Kind  string  `json:"kind"`
			USD   float64 `json:"usd"`
		} `json:"want"`
	}
	var cases []tc
	for n := 0; n < 600; n++ {
		lang := langs[rnd.Intn(len(langs))]
		codes := []string{fx.USD, fx.UAH, fx.EUR, fx.PLN, fx.BYN}
		code := codes[rnd.Intn(len(codes))]
		var c tc
		var sumAmount, sumTop, sumUSD, sumTopUSD float64
		worst := "exact"
		for i, k := 0, 2+rnd.Intn(4); i < k; i++ {
			kind := kinds[rnd.Intn(len(kinds))]
			usd := math.Exp(rnd.Float64()*9 - 7)
			e := est{Kind: kind, USD: usd, Currency: code}
			amount := usd * rate[code]
			e.Amount = &amount
			top, topUSD := amount, usd
			if kind == "up_to" {
				// The most it can cost is the figure the label shows.
				topUSD = usd * (1 + rnd.Float64()*3)
				top = topUSD * rate[code]
				e.MaxUSD, e.MaxAmount = &topUSD, &top
				e.Label = fx.Format(top, code, lang)
			} else {
				e.Label = fx.Format(amount, code, lang)
			}
			sumAmount += amount
			sumTop += top
			sumUSD += usd
			sumTopUSD += topUSD
			if rank[kind] > rank[worst] {
				worst = kind
			}
			c.Es = append(c.Es, e)
		}
		c.Want.Kind = worst
		shown, usd := sumAmount, sumUSD
		if worst == "up_to" {
			shown, usd = sumTop, sumTopUSD
		}
		c.Want.Label = fx.Format(shown, code, lang)
		c.Want.USD = usd
		if len(c.Es) == 1 {
			continue // one run is its own price, untouched
		}
		cases = append(cases, c)
	}
	// Runs priced in different currencies add up in dollars.
	mixed := tc{Es: []est{
		{Kind: "exact", USD: 0.12, Currency: fx.UAH, Amount: ptr(5.004), Label: fx.Format(5.004, fx.UAH, "uk")},
		{Kind: "about", USD: 0.5, Currency: fx.USD, Amount: ptr(0.5), Label: "$0.50"},
	}}
	mixed.Want.Kind, mixed.Want.USD, mixed.Want.Label = "about", 0.62, fx.Format(0.62, fx.USD, "en")
	cases = append(cases, mixed)

	body, _ := json.Marshal(cases)
	got := js[[]map[string]any](t, ctx, fmt.Sprintf(`const P = window.__price;
	return %s.map((c) => { const s = P.sumOf(c.es); return { label: s.label, kind: s.kind, usd: s.usd }; });`, body))
	bad := 0
	for i, c := range cases {
		g := got[i]
		if g["label"] != c.Want.Label || g["kind"] != c.Want.Kind || math.Abs(g["usd"].(float64)-c.Want.USD) > 1e-9*math.Max(1, c.Want.USD) {
			if bad++; bad <= 8 {
				in, _ := json.Marshal(c.Es)
				t.Errorf("the page sums %s to %v, Go says %q %s %.6f", in, g, c.Want.Label, c.Want.Kind, c.Want.USD)
			}
		}
	}
	if bad > 8 {
		t.Errorf("... and %d more", bad-8)
	}
}

func ptr(f float64) *float64 { return &f }
