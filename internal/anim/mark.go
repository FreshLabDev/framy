// SPDX-License-Identifier: Apache-2.0

package anim

import "math"

// The viewfinder is framy's mark (docs/design.md, Avatar): four corner
// brackets. Stripes and gaps keep the
// family's 7 : 4 proportion.
const (
	stripe = 14 // the thickness of a bracket
	gap    = 8  // the space between one photo and the next
)

// header draws the name at the top left, in lowercase like everywhere.
func (c *Canvas) header() {
	c.Text(Display, 64, "framy", 32, 84, Left, white, 1)
}

// brackets draws the four corners of a frame whose outer edge is x0, y0, x1,
// y1: arm long and stripe thick.
func (c *Canvas) brackets(x0, y0, x1, y1, arm float64, col RGB, alpha float64) {
	for _, k := range [4][2]float64{{1, 1}, {-1, 1}, {1, -1}, {-1, -1}} {
		x, y := x0, y0
		if k[0] < 0 {
			x = x1
		}
		if k[1] < 0 {
			y = y1
		}
		// Both bars of a corner go into one mask, so the corner square is
		// painted once.
		c.mask.Rect(min(x, x+k[0]*arm), min(y, y+k[1]*stripe), max(x, x+k[0]*arm), max(y, y+k[1]*stripe))
		c.mask.Rect(min(x, x+k[0]*stripe), min(y, y+k[1]*arm), max(x, x+k[0]*stripe), max(y, y+k[1]*arm))
		c.paint(c.mask, col, alpha)
	}
}

// cross draws a plus turned 45°, built of two bars: the mark of what did not
// work. r is from its middle to the end of a bar, thick the bar's width.
func (c *Canvas) cross(cx, cy, r, thick float64, col RGB, alpha float64) {
	d := thick / 2
	for _, s := range [2]float64{1, -1} {
		// A bar along the diagonal (1, s), as a polygon of its four corners.
		ux, uy := math.Sqrt2/2, s*math.Sqrt2/2
		nx, ny := -uy, ux
		c.mask.Poly([][2]float64{
			{cx - ux*r + nx*d, cy - uy*r + ny*d},
			{cx + ux*r + nx*d, cy + uy*r + ny*d},
			{cx + ux*r - nx*d, cy + uy*r - ny*d},
			{cx - ux*r - nx*d, cy - uy*r - ny*d},
		})
	}
	c.paint(c.mask, col, alpha)
}
