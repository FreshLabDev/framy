// SPDX-License-Identifier: Apache-2.0

package anim

import "math"

// The generating loop is a halftone: a grid of dots in the viewfinder that
// flows from one figure into the next. Every figure is a signed distance,
// negative inside, so one turns into the next by mixing the two.
const (
	dotPitch = 20               // from one dot's middle to the next
	dotMax   = dotPitch/2 - 1.5 // the radius of a full dot
	dotGrain = 1.6              // the radius of a dot outside every figure
	shapeFor = 1.2              // seconds a figure has, its morph in included
	morphFor = 0.5              // seconds the morph into it takes
	driftBy  = 24.0             // pixels a figure drifts each way while it is seen
)

// The middle of the figures: the middle of the viewfinder.
const (
	figX = Width / 2.0
	figY = (frameY0 + frameY1) / 2.0
)

// figure is one of the shapes the dots flow into. d is its signed distance at
// x, y from the figures' middle, at th radians of the loop: every figure moves
// a little while it holds, by whole turns of the loop, so the loop has no
// seam.
type figure struct {
	name string
	d    func(x, y, th float64) float64
}

// figures go in this order and round again. None is a logo.
var figures = []figure{
	{"blob", blob},
	{"wave", wave},
	{"heart", heart},
	{"mountains", mountains},
	{"flower", flower},
	{"crescent", crescent},
}

// generatingDur is the length of the loop: every figure once.
var generatingDur = shapeFor * float64(len(figures))

// Generating is the loop a preview turns into while a job runs: a halftone of
// dots in the viewfinder that keeps turning into a new figure, the way a
// picture comes out of an image model. It has no text and no language; the
// message's caption says what is running.
type Generating struct{}

func (Generating) Size() (int, int)  { return Width, Height }
func (Generating) Duration() float64 { return generatingDur }

func (Generating) Draw(c *Canvas, t float64) {
	c.Fill(Field)
	c.header()
	c.brackets(frameX0, frameY0, frameX1, frameY1, 72, white, 1)
	cols := (frameX1 - frameX0 - 2*frameInset) / dotPitch
	rows := (frameY1 - frameY0 - 2*frameInset) / dotPitch
	x0 := figX - float64((cols-1)*dotPitch)/2
	y0 := figY - float64((rows-1)*dotPitch)/2
	for j := range rows {
		for i := range cols {
			x, y := x0+float64(i*dotPitch), y0+float64(j*dotPitch)
			v := dotAt(i, j, x, y, t)
			c.Disc(x, y, max(dotGrain, dotMax*v), haze.Mix(white, smooth(0, 0.35, v)), 1)
		}
	}
}

// dotAt is how full dot i, j at x, y is at t: 0 outside the figure, 1 inside,
// smaller near its edge.
func dotAt(i, j int, x, y, t float64) float64 {
	u := math.Mod(t, generatingDur) / generatingDur
	th := 2 * math.Pi * u
	pos := u * float64(len(figures))
	k := int(pos) % len(figures)
	px, py := x-figX, y-figY

	// The morph runs across the grid as a wave, left to right and right to
	// left by turns, with a little of each dot's own in it, so the dots do
	// not all change at once.
	s := (pos - float64(k)) * shapeFor / morphFor
	along := (x - figX + Width/2) / Width
	if k%2 == 1 {
		along = 1 - along
	}
	o := 0.8*along + 0.2*grain(i, j)
	m := smooth(0, 1, (s-0.45*o)/0.55)
	d := shapeAt(k, px, py, th)
	if m < 1 {
		was := shapeAt((k+len(figures)-1)%len(figures), px, py, th)
		d = was + (d-was)*m
	}
	// Full inside, shrinking over the last dot and a half to the edge.
	return smooth(-dotPitch*0.4, dotPitch*1.1, -d)
}

// shapeAt is figure k's distance at px, py from the figures' middle. The
// figure drifts slowly sideways the whole time it is seen, from its morph in
// to the end of the next one's, the way the morph runs: so something moves in
// every frame, yet only the dots at its edge change, which keeps the file
// small. It turns back while it is hidden.
func shapeAt(k int, px, py, th float64) float64 {
	mid := 2 * math.Pi * (float64(k) + (shapeFor+morphFor)/(2*shapeFor)) / float64(len(figures))
	dx := driftBy * math.Sin(th-mid)
	if k%2 == 1 {
		dx = -dx
	}
	return figures[k].d(px-dx, py, th)
}

// grain is a number from 0 to 1 of dot i, j's own.
func grain(i, j int) float64 {
	h := uint32(i)*73856093 ^ uint32(j)*19349663
	h ^= h >> 13
	h *= 0x5bd1e995
	h ^= h >> 15
	return float64(h&0xffff) / 0xffff
}

// The figures. Lengths are pixels; the viewfinder's box is about 1150 by 510.

// blob is three balls melted together that roll round each other.
func blob(x, y, th float64) float64 {
	var d float64
	for k := range 3 {
		a := th*2 + float64(k)*2*math.Pi/3
		cx, cy := 170*math.Cos(a), 70*math.Sin(a)
		ball := math.Hypot(x-cx, y-cy) - [3]float64{140, 115, 95}[k]
		if k == 0 {
			d = ball
		} else {
			d = smin(d, ball, 70)
		}
	}
	return d
}

// wave is a band across the box that rolls along.
func wave(x, y, th float64) float64 {
	band := math.Abs(y-80*math.Sin(x*2*math.Pi/440+th*6)) - 52
	return max(band, math.Abs(x)-470)
}

// heart beats twice a figure.
func heart(x, y, th float64) float64 {
	s := 290 * (1 + 0.05*math.Sin(th*12))
	// Inigo Quilez's heart, in units of its size, pointing down.
	px, py := math.Abs(x)/s, (-y)/s+0.55
	if px+py > 1 {
		return (math.Hypot(px-0.25, py-0.75) - math.Sqrt2/4) * s
	}
	a := math.Hypot(px, py-1)
	b := math.Max(px+py, 0) * 0.5
	return math.Sqrt(math.Min(a*a, math.Pow(px-b, 2)+math.Pow(py-b, 2))) * sign(px-py) * s
}

// mountains are two peaks and a sun that rises behind them: the picture of
// a picture.
func mountains(x, y, th float64) float64 {
	m1 := triangle(x, y, [2]float64{-330, 210}, [2]float64{-80, -110}, [2]float64{170, 210})
	m2 := triangle(x, y, [2]float64{20, 210}, [2]float64{210, -10}, [2]float64{400, 210})
	sun := math.Hypot(x-250, y-(-150+18*math.Sin(th*6))) - 68
	return min(m1, m2, sun)
}

// flower has five petals and turns slowly.
func flower(x, y, th float64) float64 {
	r := math.Hypot(x, y)
	a := math.Atan2(y, x) + th
	return r - 230*(0.55+0.45*math.Abs(math.Cos(2.5*a)))
}

// crescent is a disc with a disc cut from it, rocking.
func crescent(x, y, th float64) float64 {
	sn, cs := math.Sincos(0.25 * math.Sin(th*6))
	x, y = cs*x+sn*y, -sn*x+cs*y
	outer := math.Hypot(x, y) - 220
	inner := math.Hypot(x-110, y+40) - 185
	return max(outer, -inner)
}

// triangle is the signed distance to the triangle a, b, c.
func triangle(x, y float64, a, b, c [2]float64) float64 {
	edge := func(p, q [2]float64) (float64, float64) {
		ex, ey := q[0]-p[0], q[1]-p[1]
		wx, wy := x-p[0], y-p[1]
		h := clamp01((wx*ex + wy*ey) / (ex*ex + ey*ey))
		dx, dy := wx-ex*h, wy-ey*h
		return dx*dx + dy*dy, ex*wy - ey*wx
	}
	d1, s1 := edge(a, b)
	d2, s2 := edge(b, c)
	d3, s3 := edge(c, a)
	d := math.Sqrt(min(d1, d2, d3))
	// Inside when the point is on the same side of every edge.
	if (s1 >= 0) == (s2 >= 0) && (s2 >= 0) == (s3 >= 0) {
		return -d
	}
	return d
}

// smin joins two distances with a round fillet of width k.
func smin(a, b, k float64) float64 {
	h := clamp01(0.5 + 0.5*(b-a)/k)
	return b + (a-b)*h - k*h*(1-h)
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
