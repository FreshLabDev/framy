// SPDX-License-Identifier: Apache-2.0
package price

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// What a video SKU is billed by. OpenRouter's pricing_skus have no schema;
// the names were read against the prices on the models' pages
// (docs/research/video.md, section 5.3): cents_per_* is in cents, the rest
// in dollars.
type quantity int

const (
	perSecond quantity = iota + 1
	perVideoToken
	perMegapixelSecond
	perImage
	minimum
)

var unitOf = map[quantity]string{
	perSecond:          "second",
	perVideoToken:      "token",
	perMegapixelSecond: "megapixel_second",
	perImage:           unitImage,
	minimum:            "run",
}

// sku is one parsed SKU: what it is billed by, and the settings it is for.
type sku struct {
	name string
	q    quantity
	rate float64
	// refsOnly: a per-image SKU for reference images, not for frames.
	refsOnly bool
	res      string
	audio    string // "yes", "no" or ""
	mode     string
	// open are the parts of the name the parser does not know, such as
	// "precise"; a SKU with any stays a maybe.
	open []string
}

// Modes a SKU can be for, and the phrases that name them.
const (
	modeText       = "text_to_video"
	modeImage      = "image_to_video"
	modeReference  = "reference"
	modeContinue   = "video_continuation"
	modeVideoInput = "with_video_input"
)

var (
	modes      = []string{modeText, modeImage, modeContinue, modeVideoInput, modeReference}
	resolution = regexp.MustCompile(`^\d+(p|k)$`)
	filler     = map[string]bool{"cents": true, "per": true, "duration": true, "seconds": true, "second": true, "output": true, "video": true, "tokens": true, "megapixel": true}
)

var errUnknownSKU = errors.New("unknown SKU")

func parseSKU(name, value string) (sku, error) {
	rate, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return sku{}, fmt.Errorf("SKU %s: %w", name, err)
	}
	parts := strings.Split(name, "_")
	has := func(w ...string) bool {
		return slices.ContainsFunc(parts, func(p string) bool { return slices.Contains(w, p) })
	}
	if has("cents") {
		rate /= 100
	}
	s := sku{name: name, rate: rate}
	switch {
	case has("minimum"):
		s.q = minimum
		return s, nil
	case has("megapixel"):
		s.q = perMegapixelSecond
	case has("video") && has("tokens"):
		s.q = perVideoToken
	case has("image", "images") && !has("duration", "second", "seconds"):
		s.q, s.refsOnly = perImage, has("reference")
		return s, nil
	case has("second", "seconds"):
		s.q = perSecond
	default:
		return sku{}, fmt.Errorf("%w %s", errUnknownSKU, name)
	}
	rest := "_" + name + "_"
	for _, phrase := range []struct{ text, audio string }{{"_without_audio_", "no"}, {"_with_audio_", "yes"}} {
		if strings.Contains(rest, phrase.text) {
			s.audio = phrase.audio
			rest = strings.Replace(rest, phrase.text, "_", 1)
		}
	}
	for _, m := range modes {
		if strings.Contains(rest, "_"+m+"_") {
			s.mode = m
			rest = strings.Replace(rest, "_"+m+"_", "_", 1)
			break
		}
	}
	for _, p := range strings.Split(strings.Trim(rest, "_"), "_") {
		switch {
		case p == "" || filler[p]:
		case resolution.MatchString(p):
			s.res = p
		default:
			s.open = append(s.open, p)
		}
	}
	return s, nil
}

// quals is what a SKU is for, as a set, so that a SKU for 720p with sound
// can be told to be more specific than one for sound alone.
func (s sku) quals() []string {
	var q []string
	if s.res != "" {
		q = append(q, "res:"+s.res)
	}
	if s.audio != "" {
		q = append(q, "audio:"+s.audio)
	}
	if s.mode != "" {
		q = append(q, "mode:"+s.mode)
	}
	return q
}

// matchSKU says whether a SKU is for the request.
func (c *calc) matchSKU(s sku) (applies, contradicted bool) {
	applies = len(s.open) == 0
	if s.res != "" {
		switch res := c.resolution(); {
		case res == "":
			applies = false
		case res != s.res:
			return false, true
		}
	}
	if s.audio != "" {
		switch a := c.audio(); {
		case a == "":
			applies = false
		case a != s.audio:
			return false, true
		}
	}
	if s.mode != "" && !c.inMode(s.mode) {
		return false, true
	}
	return applies, false
}

// audio is whether the video has sound: the toggle, or on by default where
// the model has one, as OpenRouter defaults to the model's flag.
func (c *calc) audio() string {
	switch c.setting("generate_audio") {
	case "true":
		return "yes"
	case "false":
		return "no"
	}
	if _, ok := c.of.Params["generate_audio"]; ok {
		return "yes"
	}
	return ""
}

func (c *calc) inMode(mode string) bool {
	r := c.req
	switch mode {
	case modeText:
		return r.Frames == 0 && len(r.Images) == 0 && r.Video == nil
	case modeImage:
		return r.Frames > 0
	case modeReference:
		return len(r.Images) > 0
	case modeContinue, modeVideoInput:
		return r.Video != nil
	}
	return false
}

// skus prices a video route by OpenRouter's SKUs: one SKU per thing billed,
// summed, and at least the minimum where there is one.
func (c *calc) skus(m map[string]string) {
	if len(m) == 0 {
		return
	}
	groups := map[quantity][]sku{}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		s, err := parseSKU(name, m[name])
		if err != nil {
			c.cannot(err.Error())
			c.about()
			continue
		}
		groups[s.q] = append(groups[s.q], s)
	}
	for _, q := range []quantity{perSecond, perVideoToken, perMegapixelSecond, perImage} {
		rows := groups[q]
		if len(rows) == 0 {
			continue
		}
		chosen, hiRate, ok := c.pickSKU(rows)
		if !ok {
			if q != perImage {
				c.outputUnknown("the rate for these settings")
			}
			continue
		}
		qLo, qHi, ok := c.skuQuantity(chosen)
		if !ok {
			continue
		}
		c.add(chosen.name, "", unitOf[q], chosen.rate, hiRate, qLo, qHi)
	}
	for _, s := range groups[minimum] {
		if c.lo < s.rate {
			c.lines = append(c.lines, Line{Billable: s.name, Unit: unitOf[minimum], Quantity: 1, RateUSD: s.rate, USD: s.rate - c.lo})
			c.lo = s.rate
		}
		c.hi = math.Max(c.hi, s.rate)
	}
}

// pickSKU takes, of the SKUs that are for the request, the most specific
// one. Two that are specific in different ways, such as "with sound" and
// "image to video at 720p", give the dearer, and the estimate becomes "about".
func (c *calc) pickSKU(rows []sku) (chosen sku, hiRate float64, ok bool) {
	var fits, maybe []sku
	for _, s := range rows {
		switch applies, contradicted := c.matchSKU(s); {
		case applies:
			fits = append(fits, s)
		case !contradicted:
			maybe = append(maybe, s)
		}
	}
	most := slices.DeleteFunc(slices.Clone(fits), func(s sku) bool {
		return slices.ContainsFunc(fits, func(t sku) bool { return narrower(s.quals(), t.quals()) })
	})
	if len(most) > 0 {
		chosen = slices.MaxFunc(most, func(a, b sku) int { return cmpFloat(a.rate, b.rate) })
		if slices.ContainsFunc(most, func(s sku) bool { return s.rate != chosen.rate }) {
			c.about()
		}
		ok = true
	} else if len(maybe) > 0 {
		chosen = slices.MinFunc(maybe, func(a, b sku) int { return cmpFloat(a.rate, b.rate) })
		ok = true
	}
	hiRate = chosen.rate
	for _, s := range maybe {
		hiRate = math.Max(hiRate, s.rate)
	}
	return chosen, hiRate, ok
}

// narrower reports whether a is a strict subset of b.
func narrower(a, b []string) bool {
	if len(a) >= len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func (c *calc) skuQuantity(s sku) (lo, hi float64, ok bool) {
	switch s.q {
	case perSecond:
		lo, hi, ok := c.seconds()
		if !ok {
			c.outputUnknown("the length of the video")
		}
		return lo, hi, ok
	case perVideoToken:
		// Seedance: width × height × 24 × seconds / 1024, the formula on
		// OpenRouter's model pages.
		sLo, sHi, ok := c.seconds()
		if !ok {
			c.outputUnknown("the length of the video")
			return 0, 0, false
		}
		w, h := c.videoSize()
		per := float64(w*h) * 24 / 1024
		return per * sLo, per * sHi, true
	case perMegapixelSecond:
		v := c.req.Video
		if v == nil {
			c.outputUnknown("the source video")
			return 0, 0, false
		}
		fLo, fHi := c.factor()
		mp := float64(v.Width*v.Height) / 1e6
		return mp * fLo * fLo * v.Seconds, mp * fHi * fHi * v.Seconds, true
	case perImage:
		n := float64(len(c.req.Images))
		if !s.refsOnly {
			n += float64(c.req.Frames)
		}
		return n, n, true
	}
	return 0, 0, false
}

// factor is the upscale factor: the chosen one, or the model's range.
func (c *calc) factor() (lo, hi float64) {
	if f, ok := c.number("upscale_factor"); ok && f > 0 {
		return f, f
	}
	if p, ok := c.of.Params["upscale_factor"]; ok && p.Min != nil && p.Max != nil {
		return *p.Min, *p.Max
	}
	return 1, 1
}

// videoSize is the output's width and height: the chosen size, else the
// size the model lists for the resolution and the aspect ratio, else the
// resolution's height at that ratio.
func (c *calc) videoSize() (w, h int) {
	if w, h, ok := size(c.setting("size")); ok {
		return w, h
	}
	p := height(c.resolution())
	r, ok := ratio(c.setting("aspect_ratio"))
	if !ok {
		r = 16.0 / 9
	}
	if listed, ok := c.of.Params["size"]; ok && p > 0 {
		var best [2]int
		bestScore := math.Inf(1)
		for _, v := range listed.Values {
			w, h, ok := size(fmt.Sprint(v))
			if !ok || math.Abs(float64(w)/float64(h)/r-1) > 0.03 {
				continue
			}
			// Some models keep the short side, others the area of 16:9.
			score := math.Abs(float64(w*h) - float64(p*p)*16/9)
			if min(w, h) == p {
				score = -1
			}
			if score < bestScore {
				best, bestScore = [2]int{w, h}, score
			}
		}
		if best[0] > 0 {
			return best[0], best[1]
		}
	}
	c.about()
	if p == 0 {
		p = 720
	}
	if r >= 1 {
		return int(math.Round(float64(p) * r)), p
	}
	return p, int(math.Round(float64(p) / r))
}

// height is the short side a video resolution names: 720p is 720, 2K 1440,
// 4K 2160.
func height(res string) int {
	switch res {
	case "1k":
		return 1080
	case "2k":
		return 1440
	case "4k":
		return 2160
	}
	n, err := strconv.Atoi(strings.TrimSuffix(res, "p"))
	if err != nil {
		return 0
	}
	return n
}
