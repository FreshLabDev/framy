// SPDX-License-Identifier: Apache-2.0

package anim

import "math"

// generatingDur is the length of the loop. Everything in it that moves does
// a whole number of turns, so its end leads into its start.
const generatingDur = 6.0

// The halftone of the generating loop: a grid of dots in the viewfinder.
const (
	dotPitch = 20               // from one dot's middle to the next
	dotMax   = dotPitch/2 - 1.5 // the radius of a full dot
	markR    = 196.0            // the spark the dots gather into, middle to point
	markX    = 360.0            // its middle
	markY    = (frameY0 + frameY1) / 2.0
)

// Generating is the loop a preview turns into while a job runs, the way an
// image model works: a halftone of pale dots drifts like noise, gathers into
// framy's spark in white, holds it while the viewfinder focuses, and lets it
// go into noise again. It has no text and no language; the message's caption
// says what is running.
type Generating struct{}

func (Generating) Size() (int, int)  { return Side, Side }
func (Generating) Duration() float64 { return generatingDur }

// gather is how far the dots are gathered into the spark at u, the share of
// the loop gone: 0 in the noise, 1 when the spark has formed.
func gather(u float64) float64 {
	return smooth(0.28, 0.5, u) - smooth(0.7, 0.92, u)
}

func (Generating) Draw(c *Canvas, t float64) {
	c.Fill(Field)
	c.header()
	u := math.Mod(t, generatingDur) / generatingDur
	th := 2 * math.Pi * u
	g := gather(u)

	// The viewfinder closes a little as the spark forms, as a camera does
	// when it has focused.
	in := 8 * g
	c.brackets(frameX0+in, frameY0+in, frameX1-in, frameY1-in, 72, white, 1)

	x0 := float64(frameX0+frameInset) + dotPitch/2.0
	y0 := float64(frameY0+frameInset) + dotPitch/2.0
	cols := (frameX1 - frameX0 - 2*frameInset) / dotPitch
	rows := (frameY1 - frameY0 - 2*frameInset) / dotPitch
	// The grid sits in the middle of the box.
	x0 += float64(frameX1-frameX0-2*frameInset-cols*dotPitch) / 2
	y0 += float64(frameY1-frameY0-2*frameInset-rows*dotPitch) / 2
	for j := range rows {
		y := y0 + float64(j*dotPitch)
		for i := range cols {
			x := x0 + float64(i*dotPitch)
			n := noise(i, j, x, y, th)
			// The noise thins out towards the edge of the box, so it reads as
			// a picture not yet there rather than a pattern.
			edge := min(x-x0, x0+float64((cols-1)*dotPitch)-x, y-y0, y0+float64((rows-1)*dotPitch)-y)
			n *= 0.75 * (0.3 + 0.7*smooth(0, 3*dotPitch, edge))
			// While it holds, a ripple runs out through the spark, so that
			// the loop never stands still.
			s := sparkShape(x, y)
			ripple := 0.88 + 0.12*math.Sin(3*th-0.035*math.Hypot(x-markX, y-markY))
			v := n + (s*ripple-n)*g
			if r := dotMax * v; r > 1 {
				// The noise is a paler plum; the spark comes out white.
				c.Disc(x, y, r, haze.Mix(white, s*g), 1)
			}
		}
	}
}

// noise is the size of dot i, j at x, y, 0 to 1, at th radians of the loop:
// soft clouds made of three waves that cross the grid in different ways, and
// every dot breathing a little in its own phase, once a loop.
func noise(i, j int, x, y, th float64) float64 {
	flow := (math.Sin(0.016*x+0.009*y+th) +
		math.Sin(-0.011*x+0.017*y-th+1.1) +
		math.Sin(0.007*x-0.013*y+th+2.3)) / 3
	h := uint32(i)*73856093 ^ uint32(j)*19349663
	h ^= h >> 13
	h *= 0x5bd1e995
	h ^= h >> 15
	own := math.Sin(th + float64(h&0xffff)/0x10000*2*math.Pi)
	return smooth(0.05, 0.85, 0.5+0.42*flow+0.18*own)
}

// sparkShape is 1 inside the spark of the mark at the middle of the frame, 0
// outside it, with a soft edge a dot wide.
func sparkShape(x, y float64) float64 {
	dx, dy := math.Abs(x-markX), math.Abs(y-markY)
	// The spark is four diamonds' points: in the first quadrant its edge runs
	// from the point (markR, 0) to the waist (w, w) and on to (0, markR).
	w := markR * 0.3 / math.Sqrt2
	if dy > dx {
		dx, dy = dy, dx
	}
	// The distance to the edge from (markR, 0) to (w, w), negative inside.
	ex, ey := w-markR, w
	l := math.Hypot(ex, ey)
	nx, ny := ey/l, -ex/l // the outward normal
	d := (dx-markR)*nx + dy*ny
	return 1 - smooth(-dotPitch*0.6, dotPitch*0.6, d)
}
