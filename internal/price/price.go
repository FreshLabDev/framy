// SPDX-License-Identifier: Apache-2.0

// Package price estimates what a run costs before it starts, from the rates
// in the catalog, and keeps the rates it used, so the actual cost can be
// compared with them after the run.
package price

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Kind is how much an estimate can be trusted.
type Kind string

const (
	// Exact: the rates and the quantities are known before the run.
	Exact Kind = "exact"
	// About: a formula, a token count or a rounding makes it approximate.
	About Kind = "about"
	// UpTo: MaxUSD is the most the run can cost; the provider decides the
	// rest, as with a length the model picks.
	UpTo Kind = "up_to"
	// From: USD is the least the run costs, and the provider sets the rest
	// after it.
	From Kind = "from"
	// Unknown: the output itself has no rate framy can read; the price is
	// known only after the run.
	Unknown Kind = "unknown"
)

var rank = map[Kind]int{Exact: 0, About: 1, UpTo: 2, From: 3, Unknown: 4}

func worse(a, b Kind) Kind {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Image is the size of an input image after framy's input pipeline.
type Image struct{ Width, Height int }

// Video is an attached source video, as ffprobe reads it.
type Video struct {
	Seconds       float64
	Width, Height int
}

// Request is what the person is about to run. Settings carries the value of
// every setting the composer shows, defaults included; a setting that
// changes the price and is missing turns the estimate into a range.
type Request struct {
	Settings map[string]any
	Prompt   string
	// Images are the reference images.
	Images []Image
	// Frames is how many images go as the first and the last frame.
	Frames int
	Video  *Video
}

// Line is one priced part of a run: the billable or the SKU, its rate and
// how much of it the run takes. Lines are the snapshot a job keeps.
type Line struct {
	Billable string  `json:"billable"`
	Variant  string  `json:"variant,omitempty"`
	Unit     string  `json:"unit"`
	Quantity float64 `json:"quantity"`
	RateUSD  float64 `json:"rate_usd"`
	USD      float64 `json:"usd"`
}

// Estimate is the price of a run before it starts.
type Estimate struct {
	Offer string `json:"offer"`
	// Route is the upstream the estimate came from, the cheapest one when an
	// offer has several.
	Route string `json:"route,omitempty"`
	// USD is the likely price; MaxUSD the most it can be, equal to USD when
	// nothing is open.
	USD    float64 `json:"usd"`
	MaxUSD float64 `json:"max_usd"`
	Kind   Kind    `json:"kind"`
	// Unknown names the parts that could not be priced, such as the tokens
	// of the input images.
	Unknown []string `json:"unknown,omitempty"`
	// Note says why the price is only a floor, when Kind is From.
	Note string `json:"note,omitempty"`
	// Discount is the share the route takes off its listed rates, 0.15 for
	// 15%, and USD and MaxUSD are after it. ListUSD and ListMaxUSD are the
	// same figures at the listed rates, for a price shown struck through;
	// both are zero without a discount.
	Discount   float64 `json:"discount,omitempty"`
	ListUSD    float64 `json:"list_usd,omitempty"`
	ListMaxUSD float64 `json:"list_max_usd,omitempty"`
	// Lines are at the listed rates, before the discount.
	Lines []Line `json:"lines,omitempty"`
}

// Of estimates a run of the offer of model m.
func Of(m catalog.Model, of catalog.Offer, req Request) Estimate {
	if len(of.Routes) == 0 {
		return Estimate{Offer: of.ID, Kind: Unknown, Unknown: []string{"no route runs the offer"}}
	}
	var best Estimate
	for i, r := range of.Routes {
		e := route(m, of, r, req)
		switch {
		case i == 0:
			best = e
		case e.USD < best.USD:
			e.MaxUSD = math.Max(e.MaxUSD, best.MaxUSD)
			e.Kind = worse(e.Kind, best.Kind)
			best = e
		default:
			best.MaxUSD = math.Max(best.MaxUSD, e.MaxUSD)
			best.Kind = worse(best.Kind, e.Kind)
		}
	}
	if best.Kind == Exact && best.MaxUSD > best.USD {
		// Routes at different prices: OpenRouter takes the cheapest, but may
		// fall back to another.
		best.Kind = About
	}
	if best.Discount > 0 {
		// The most may come from a dearer route; the listed figure is never
		// below the one it is shown against.
		best.ListMaxUSD = math.Max(best.ListMaxUSD, best.MaxUSD)
	}
	return best
}

// calc is one route's estimate in progress: lo is the likely total, hi the
// most it can be.
type calc struct {
	m       catalog.Model
	of      catalog.Offer
	hints   catalog.PriceHints
	req     Request
	lo, hi  float64
	kind    Kind
	unknown []string
	lines   []Line
}

func route(m catalog.Model, of catalog.Offer, r catalog.Route, req Request) Estimate {
	c := &calc{m: m, of: of, req: req, kind: Exact}
	if m.Traits.Price != nil {
		c.hints = *m.Traits.Price
	}
	c.prices(r.Prices)
	c.skus(r.SKUs)
	e := Estimate{Offer: of.ID, Route: r.Provider, USD: c.lo, MaxUSD: math.Max(c.hi, c.lo), Kind: c.kind, Unknown: c.unknown, Lines: c.lines}
	if c.hints.From != "" && e.Kind != Unknown {
		e.Kind, e.Note = From, c.hints.From
	}
	if len(e.Unknown) > 0 && e.Kind == Exact {
		e.Kind = About
	}
	if e.Kind == Exact {
		e.MaxUSD = e.USD
	}
	if d := r.Discount; catalog.Discounted(d) {
		// OpenRouter shows the listed rate struck through and bills the
		// discounted one (docs/design.md, Price and currency).
		e.Discount, e.ListUSD, e.ListMaxUSD = d, e.USD, e.MaxUSD
		e.USD *= 1 - d
		e.MaxUSD *= 1 - d
	}
	return e
}

// add records a line priced at rate for a quantity between qLo and qHi, and
// for a rate that may go up to rateHi.
func (c *calc) add(billable, variant, unit string, rate, rateHi, qLo, qHi float64) {
	if qLo == 0 && qHi == 0 {
		return
	}
	c.lines = append(c.lines, Line{Billable: billable, Variant: variant, Unit: unit, Quantity: qLo, RateUSD: rate, USD: rate * qLo})
	c.lo += rate * qLo
	c.hi += math.Max(rate, rateHi) * qHi
	if rateHi > rate || qHi > qLo {
		c.kind = worse(c.kind, UpTo)
	}
}

func (c *calc) about()                 { c.kind = worse(c.kind, About) }
func (c *calc) cannot(what string)     { c.unknown = append(c.unknown, what) }
func (c *calc) outputUnknown(w string) { c.cannot(w); c.kind = Unknown }

// setting returns the chosen value of a setting as lower-case text, or "".
func (c *calc) setting(name string) string {
	v, ok := c.req.Settings[name]
	if !ok || v == nil {
		return ""
	}
	return strings.ToLower(fmt.Sprint(v))
}

func (c *calc) number(name string) (float64, bool) {
	switch v := c.req.Settings[name].(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	return 0, false
}

// count is how many results a run makes.
func (c *calc) count() float64 {
	if n, ok := c.number("n"); ok && n >= 1 {
		return n
	}
	return 1
}

// resolution is the chosen resolution, or the only one the offer has.
func (c *calc) resolution() string {
	if r := c.setting("resolution"); r != "" {
		return r
	}
	if p, ok := c.of.Params["resolution"]; ok && len(p.Values) == 1 {
		return strings.ToLower(fmt.Sprint(p.Values[0]))
	}
	return ""
}

// enumHas reports whether some enum setting of the offer has the value, and
// whether that setting is chosen with another value.
func (c *calc) enumHas(value string) (has, other bool) {
	for name, p := range c.of.Params {
		if p.Type != catalog.Enum {
			continue
		}
		if !slices.ContainsFunc(p.Values, func(v any) bool { return strings.EqualFold(fmt.Sprint(v), value) }) {
			continue
		}
		has = true
		if chosen := c.setting(name); chosen != "" && chosen != value {
			other = true
		}
	}
	return has, other
}

// seconds is the length of the output video, as a range.
func (c *calc) seconds() (lo, hi float64, ok bool) {
	if d, ok := c.number("duration"); ok && d > 0 {
		return d, d, true
	}
	v := c.req.Video
	if s := c.m.Traits.SourceVideo; s != nil && s.KeepsLength && v != nil {
		return v.Seconds, v.Seconds, true
	}
	if l := c.m.Traits.PicksLength; l != nil {
		return float64(l.Min), float64(l.Max), true
	}
	if v != nil {
		// An editor that does not say whether it keeps the length.
		c.about()
		return v.Seconds, v.Seconds, true
	}
	if p, ok := c.of.Params["duration"]; ok && len(p.Values) > 0 {
		var ds []float64
		for _, x := range p.Values {
			if f, err := strconv.ParseFloat(fmt.Sprint(x), 64); err == nil {
				ds = append(ds, f)
			}
		}
		if len(ds) > 0 {
			return slices.Min(ds), slices.Max(ds), true
		}
	}
	return 0, 0, false
}

func round16(x float64) int { return int(math.Round(x/16)) * 16 }

// ratio parses "16:9" as 16/9.
func ratio(s string) (float64, bool) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, false
	}
	return x / y, true
}

// size parses "1280x720".
func size(s string) (w, h int, ok bool) {
	a, b, ok := strings.Cut(strings.ToLower(s), "x")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	return w, h, err1 == nil && err2 == nil && w > 0 && h > 0
}
