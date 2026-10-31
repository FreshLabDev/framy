// SPDX-License-Identifier: Apache-2.0
package price

import "math"

// openAITokens is the number of output tokens of an OpenAI image of w×h at
// the quality whose base is b: the formula of OpenAI's own calculator
// (docs/research/images.md, section 3.1, and openai_image_tokens.py there).
// It gives the published $0.053 for gpt-image-2 at medium 1024×1024 and
// OpenRouter's $0.13 for high 1536×864.
func openAITokens(w, h, b int) int {
	long, short := float64(max(w, h)), float64(min(w, h))
	o := float64(b)
	s := o / (long / short)
	l := math.Floor(s)
	u := math.Floor(s + 0.5)
	if s-l == 0.5 {
		// An exact half goes to the even neighbour.
		u = l + math.Mod(l, 2)
	}
	// The calculator multiplies the base by the rounded share on the short
	// side, whichever side that is.
	d := o * u
	return int(math.Ceil(d * (2_000_000 + float64(w*h)) / 4_000_000))
}

// openAIInputTokens is how many tokens GPT Image 2 and 2.5 take for an input
// image of w×h: one a 32 px patch, at most budget. OpenAI does not publish
// the count; this is the one worked out on OpenAI's forum ("OpenAI must
// document the input image pricing of gpt-image-2", April 2026), and a run
// on the test bot matched it: a 1280×853 photo took 1,080 tokens.
func openAIInputTokens(w, h, budget int) int {
	const patch = 32
	fw, fh := float64(w), float64(h)
	// An edit magnifies a small image to a long side of 1024, at most twice.
	k := min(2, max(1, 1024/max(fw, fh)))
	fw, fh = math.Floor(fw*k+1e-9), math.Floor(fh*k+1e-9)
	// A canvas longer than 3:1 is counted as 3:1.
	pw, ph := ceilEps(fw/patch), ceilEps(fh/patch)
	switch {
	case pw > 3*ph:
		fh = ceilEps(pw/3) * patch
	case ph > 3*pw:
		fw = ceilEps(ph/3) * patch
	}
	pw, ph = ceilEps(fw/patch), ceilEps(fh/patch)
	if pw*ph <= float64(budget) {
		return int(pw * ph)
	}
	// Over the budget the canvas shrinks to fit it, then a little more, so
	// that whole patches cover it.
	r := math.Sqrt(patch * patch * float64(budget) / (fw * fh))
	x, y := fw*r/patch, fh*r/patch
	r *= min(math.Floor(x+1e-9)/x, math.Floor(y+1e-9)/y)
	return int(ceilEps(fw*r/patch) * ceilEps(fh*r/patch))
}

// ceilEps rounds up, leaving a float that missed a whole number by a hair.
func ceilEps(x float64) float64 { return math.Ceil(x - 1e-9) }

// openAISize is the size of an OpenAI image: the chosen size on the direct
// API; on OpenRouter, which takes an aspect ratio, 1024×1024 for a square and
// a long side of 1536 otherwise, as 1536×1024 and OpenRouter's own 1536×864
// for 16:9 show.
func (c *calc) openAISize() (w, h int) {
	if w, h, ok := size(c.setting("size")); ok {
		return w, h
	}
	r, ok := ratio(c.setting("aspect_ratio"))
	if !ok || r == 1 {
		return 1024, 1024
	}
	if r > 1 {
		return 1536, round16(1536 / r)
	}
	return round16(1536 * r), 1536
}
