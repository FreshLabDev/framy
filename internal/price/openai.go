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
