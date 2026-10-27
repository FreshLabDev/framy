// SPDX-License-Identifier: Apache-2.0
package price

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Billables and units of the price lines framy reads. OpenRouter publishes
// them for image endpoints; the hand-written descriptors use the same form.
const (
	outputImage    = "output_image"
	outputVideo    = "output_video"
	outputText     = "output_text"
	inputImage     = "input_image"
	inputText      = "input_text"
	inputVideo     = "input_video"
	inputReference = "input_reference"
	inputFont      = "input_font"

	unitImage     = "image"
	unitMegapixel = "megapixel"
	unitToken     = "token"
	unitRequest   = "request"
)

// knownLine reports whether the estimate can read a price line.
func knownLine(billable, unit string) bool {
	switch billable + "/" + unit {
	case outputImage + "/" + unitImage, outputImage + "/" + unitMegapixel, outputImage + "/" + unitToken, outputImage + "/" + unitRequest,
		inputImage + "/" + unitImage, inputImage + "/" + unitMegapixel, inputImage + "/" + unitToken,
		inputReference + "/" + unitImage, inputReference + "/" + unitRequest,
		inputFont + "/" + unitImage, inputText + "/" + unitToken, outputText + "/" + unitToken,
		inputVideo + "/" + unitToken, outputVideo + "/" + unitToken:
		return true
	}
	return false
}

// prices reads the lines of an image route or of a direct video offer.
func (c *calc) prices(rows []catalog.Price) {
	type key struct{ billable, unit string }
	groups := map[key][]catalog.Price{}
	var order []key
	for _, p := range rows {
		k := key{p.Billable, p.Unit}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p)
	}
	for _, k := range order {
		if _, skip := c.hints.Skip[k.billable]; skip {
			continue
		}
		if !knownLine(k.billable, k.unit) {
			c.cannot(k.billable + " per " + k.unit)
			if strings.HasPrefix(k.billable, "output_") {
				c.kind = Unknown
			}
			continue
		}
		chosen, hiRate, ok := c.choose(groups[k])
		if !ok {
			if k.billable == outputImage && k.unit != unitRequest {
				c.outputUnknown("the price of this resolution")
			}
			continue
		}
		qLo, qHi, ok := c.quantity(k.billable, k.unit)
		if !ok {
			continue
		}
		c.add(k.billable, chosen.Variant, k.unit, chosen.CostUSD, hiRate, qLo, qHi)
	}
}

// choose picks the line of a group that applies to the request: the most
// specific variant whose parts are all chosen, else the line without a
// variant. hiRate is the dearest line that may apply too.
func (c *calc) choose(rows []catalog.Price) (chosen catalog.Price, hiRate float64, ok bool) {
	var maybe []catalog.Price
	best := -1
	for _, p := range rows {
		if p.Variant == "" {
			if best < 0 {
				chosen, ok = p, true
			}
			continue
		}
		spec, applies, contradicted := c.variant(p.Variant)
		switch {
		case applies && (spec > best || spec == best && p.CostUSD > chosen.CostUSD):
			chosen, ok, best = p, true, spec
		case !applies && !contradicted:
			maybe = append(maybe, p)
		}
	}
	if !ok && len(maybe) > 0 {
		chosen = slices.MinFunc(maybe, func(a, b catalog.Price) int { return cmpFloat(a.CostUSD, b.CostUSD) })
		ok = true
	}
	hiRate = chosen.CostUSD
	for _, p := range maybe {
		hiRate = math.Max(hiRate, p.CostUSD)
	}
	return chosen, hiRate, ok
}

// variant says whether a price variant applies. The overlay's conditions
// come first; otherwise every part of the name, split at "_", must be a
// chosen value, as low_1k is quality low at resolution 1K. A part that is a
// value of a setting chosen otherwise rules the variant out; a part that
// matches nothing leaves it open.
func (c *calc) variant(name string) (spec int, applies, contradicted bool) {
	if when, ok := c.hints.Variants[name]; ok {
		applies = true
		for param, want := range when {
			if _, offered := c.of.Params[param]; !offered {
				return 0, false, true
			}
			chosen := c.setting(param)
			switch {
			case chosen == "":
				applies = false
			case want == catalog.Any:
			case !strings.EqualFold(chosen, strings.ToLower(toString(want))):
				return 0, false, true
			}
		}
		return len(when), applies, false
	}
	parts := strings.Split(strings.ToLower(name), "_")
	applies = true
	for _, part := range parts {
		if c.isChosen(part) {
			continue
		}
		applies = false
		if _, other := c.enumHas(part); other {
			return 0, false, true
		}
	}
	return len(parts), applies, false
}

func (c *calc) isChosen(value string) bool {
	for name := range c.req.Settings {
		if c.setting(name) == value {
			return true
		}
	}
	return false
}

// quantity is how much of a billable the run takes, as a range.
func (c *calc) quantity(billable, unit string) (lo, hi float64, ok bool) {
	n := c.count()
	refs := float64(len(c.req.Images))
	switch billable + "/" + unit {
	case outputImage + "/" + unitImage:
		return n, n, true
	case outputImage + "/" + unitRequest:
		return 1, 1, true
	case outputImage + "/" + unitMegapixel:
		c.about()
		mp := c.outputPixels() / 1e6
		return n * mp, n * mp, true
	case outputImage + "/" + unitToken:
		lo, hi, ok := c.outputTokens()
		if !ok {
			c.outputUnknown("the tokens of the output image")
			return 0, 0, false
		}
		c.about()
		return n * lo, n * hi, true
	case inputImage + "/" + unitImage, inputReference + "/" + unitImage:
		return refs, refs, true
	case inputReference + "/" + unitRequest:
		if refs > 0 {
			return 1, 1, true
		}
		return 0, 0, true
	case inputImage + "/" + unitMegapixel:
		c.about()
		var mp float64
		for _, im := range c.req.Images {
			mp += float64(im.Width*im.Height) / 1e6
		}
		return mp, mp, true
	case inputImage + "/" + unitToken:
		images := refs + float64(c.req.Frames)
		if images == 0 {
			return 0, 0, true
		}
		per, ok := c.hints.Tokens[inputImage][catalog.Any]
		if !ok {
			c.cannot("the tokens of the input images")
			return 0, 0, false
		}
		c.about()
		return images * float64(per), images * float64(per), true
	case inputFont + "/" + unitImage:
		// framy sends no font files yet.
		return 0, 0, true
	case inputText + "/" + unitToken:
		// About two runes a token, more than English takes, so the estimate
		// does not come out low for Cyrillic.
		t := math.Ceil(float64(utf8.RuneCountInString(c.req.Prompt)) / 2)
		if t > 0 {
			c.about()
		}
		return t, t, true
	case outputText + "/" + unitToken:
		c.cannot("the text and thinking tokens")
		return 0, 0, false
	case inputVideo + "/" + unitToken:
		if c.req.Video != nil {
			c.cannot("the tokens of the input video")
		}
		return 0, 0, false
	case outputVideo + "/" + unitToken:
		per, ok := c.hints.Tokens[outputVideo][c.resolution()]
		if !ok {
			per, ok = c.hints.Tokens[outputVideo][catalog.Any]
		}
		sLo, sHi, okS := c.seconds()
		if !ok || !okS {
			c.outputUnknown("the tokens of the output video")
			return 0, 0, false
		}
		return float64(per) * sLo, float64(per) * sHi, true
	}
	return 0, 0, false
}

// outputTokens is how many tokens one output image takes: OpenAI's formula,
// or the overlay's count by resolution.
func (c *calc) outputTokens() (lo, hi float64, ok bool) {
	if base := c.hints.OpenAITokens; len(base) > 0 {
		w, h := c.openAISize()
		if q := c.setting("quality"); q != "" {
			b, ok := base[q]
			if !ok {
				return 0, 0, false
			}
			t := float64(openAITokens(w, h, b))
			return t, t, true
		}
		// No quality chosen: anything from the lowest to the highest.
		var ts []float64
		for _, b := range base {
			ts = append(ts, float64(openAITokens(w, h, b)))
		}
		return slices.Min(ts), slices.Max(ts), true
	}
	counts := c.hints.Tokens[outputImage]
	if len(counts) == 0 {
		return 0, 0, false
	}
	res := c.resolution()
	for k, n := range counts {
		if strings.EqualFold(k, res) {
			return float64(n), float64(n), true
		}
	}
	if n, ok := counts[catalog.Any]; ok {
		return float64(n), float64(n), true
	}
	if res == "" {
		var ts []float64
		for _, n := range counts {
			ts = append(ts, float64(n))
		}
		return slices.Min(ts), slices.Max(ts), true
	}
	return 0, 0, false
}

// outputPixels is the area of one output image: the chosen size, or the
// resolution's square, or a megapixel.
func (c *calc) outputPixels() float64 {
	if w, h, ok := size(c.setting("size")); ok {
		return float64(w * h)
	}
	if s, ok := side(c.resolution()); ok {
		return s * s
	}
	return 1024 * 1024
}

// side is the side of a resolution's square: 1K is 1024, 1.5K 1536, 768 is
// 768.
func side(res string) (float64, bool) {
	if res == "" {
		return 0, false
	}
	k := strings.HasSuffix(res, "k")
	f, err := strconv.ParseFloat(strings.TrimSuffix(res, "k"), 64)
	if err != nil || f <= 0 {
		return 0, false
	}
	if k {
		f *= 1024
	}
	return f, true
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return ""
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
