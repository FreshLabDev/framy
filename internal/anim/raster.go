// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"image"
	"image/draw"
	"math"
	"strconv"

	"github.com/go-text/typesetting/language"
	"golang.org/x/image/vector"
)

// RGB is a colour in sRGB, each channel 0..1. Colours blend in sRGB, as the
// browser canvas the family's pictures were designed in does.
type RGB struct{ R, G, B float32 }

// Hex parses "#RRGGBB".
func Hex(s string) RGB {
	n, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil || len(s) != 7 {
		panic("anim: bad colour " + s)
	}
	return RGB{float32(n>>16) / 255, float32(n>>8&255) / 255, float32(n&255) / 255}
}

// Mix goes from a to b as t goes from 0 to 1.
func (a RGB) Mix(b RGB, t float64) RGB {
	f := float32(t)
	return RGB{a.R + (b.R-a.R)*f, a.G + (b.G-a.G)*f, a.B + (b.B-a.B)*f}
}

// Scale darkens a colour: 0.72 keeps 72% of every channel.
func (a RGB) Scale(k float64) RGB {
	f := float32(k)
	return RGB{a.R * f, a.G * f, a.B * f}
}

// Canvas is one frame being drawn: opaque sRGB pixels and the scratch space
// its shapes are rasterised in. A canvas belongs to one goroutine.
type Canvas struct {
	W, H int
	Pix  []float32         // r, g, b per pixel, rows top to bottom
	Lang language.Language // of the text drawn on it
	mask *Mask             // scratch for one shape or layer at a time
}

func NewCanvas(w, h int) *Canvas {
	return &Canvas{W: w, H: h, Pix: make([]float32, 3*w*h), mask: NewMask(w, h)}
}

// Fill paints the whole canvas one colour.
func (c *Canvas) Fill(col RGB) {
	for i := 0; i < len(c.Pix); i += 3 {
		c.Pix[i], c.Pix[i+1], c.Pix[i+2] = col.R, col.G, col.B
	}
}

// paint blends col over the canvas by the mask's coverage times alpha, then
// clears the mask.
func (c *Canvas) paint(m *Mask, col RGB, alpha float64) {
	a := float32(alpha)
	if a > 0 {
		for y := m.box.Min.Y; y < m.box.Max.Y; y++ {
			row := y * m.W
			for x := m.box.Min.X; x < m.box.Max.X; x++ {
				k := m.A[row+x] * a
				if k <= 0 {
					continue
				}
				p := c.Pix[3*(row+x) : 3*(row+x)+3 : 3*(row+x)+3]
				p[0] += (col.R - p[0]) * k
				p[1] += (col.G - p[1]) * k
				p[2] += (col.B - p[2]) * k
			}
		}
	}
	m.Clear()
}

// Disc paints a filled circle.
func (c *Canvas) Disc(cx, cy, r float64, col RGB, alpha float64) {
	c.mask.Disc(cx, cy, r)
	c.paint(c.mask, col, alpha)
}

// Ring paints the stroke of a circle.
func (c *Canvas) Ring(cx, cy, r, width float64, col RGB, alpha float64) {
	c.mask.Ring(cx, cy, r, width)
	c.paint(c.mask, col, alpha)
}

// Rect paints the rectangle x0, y0, x1, y1; its edges may fall between pixels.
func (c *Canvas) Rect(x0, y0, x1, y1 float64, col RGB, alpha float64) {
	c.mask.Rect(x0, y0, x1, y1)
	c.paint(c.mask, col, alpha)
}

// Poly paints the polygon through pts.
func (c *Canvas) Poly(pts [][2]float64, col RGB, alpha float64) {
	c.mask.Poly(pts)
	c.paint(c.mask, col, alpha)
}

// RGBA writes the frame as 8-bit RGBA, rounding to the nearest level.
func (c *Canvas) RGBA(dst []byte) {
	for i, j := 0, 0; i < len(c.Pix); i, j = i+3, j+4 {
		dst[j] = level(c.Pix[i])
		dst[j+1] = level(c.Pix[i+1])
		dst[j+2] = level(c.Pix[i+2])
		dst[j+3] = 255
	}
}

// RGB24 writes the frame as 8-bit RGB, three bytes a pixel.
func (c *Canvas) RGB24(dst []byte) {
	for i, v := range c.Pix {
		dst[i] = level(v)
	}
}

// Image returns the frame as an image.
func (c *Canvas) Image() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, c.W, c.H))
	c.RGBA(img.Pix)
	return img
}

func level(v float32) byte {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 255
	}
	return byte(v*255 + 0.5)
}

// Mask is coverage, 0..1 per pixel. Shapes added to one mask merge by taking
// the larger coverage, so shapes of one layer blend once where they touch.
type Mask struct {
	W, H int
	A    []float32
	box  image.Rectangle // where A may be non-zero
	z    vector.Rasterizer
	pix  []byte
}

func NewMask(w, h int) *Mask { return &Mask{W: w, H: h, A: make([]float32, w*h)} }

// Clear zeroes the mask where it was drawn on.
func (m *Mask) Clear() {
	for y := m.box.Min.Y; y < m.box.Max.Y; y++ {
		clear(m.A[y*m.W+m.box.Min.X : y*m.W+m.box.Max.X])
	}
	m.box = image.Rectangle{}
}

// span clips the pixel range a shape touches, x0..x1 and y0..y1 in canvas
// units, to the mask and grows the drawn box by it.
func (m *Mask) span(x0, y0, x1, y1 float64) image.Rectangle {
	r := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1))).
		Intersect(image.Rect(0, 0, m.W, m.H))
	if !r.Empty() {
		m.box = m.box.Union(r)
	}
	return r
}

func (m *Mask) put(i int, k float32) {
	if k > m.A[i] {
		m.A[i] = k
	}
}

// cover turns a signed distance in pixels, negative inside, into coverage.
func cover(d float64) float32 {
	switch {
	case d <= -0.5:
		return 1
	case d >= 0.5:
		return 0
	}
	return float32(0.5 - d)
}

func (m *Mask) Disc(cx, cy, r float64) {
	if r <= 0 {
		return
	}
	b := m.span(cx-r-1, cy-r-1, cx+r+1, cy+r+1)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		dy := float64(y) + 0.5 - cy
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x) + 0.5 - cx
			m.put(y*m.W+x, cover(math.Sqrt(dx*dx+dy*dy)-r))
		}
	}
}

// Ring is the stroke of a circle of radius r, width wide.
func (m *Mask) Ring(cx, cy, r, width float64) {
	o := r + width/2 + 1
	b := m.span(cx-o, cy-o, cx+o, cy+o)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		dy := float64(y) + 0.5 - cy
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x) + 0.5 - cx
			m.put(y*m.W+x, cover(math.Abs(math.Sqrt(dx*dx+dy*dy)-r)-width/2))
		}
	}
}

// Rect adds the rectangle x0, y0, x1, y1. A pixel's coverage is the part of
// it the rectangle holds, so an edge between pixels is a soft one.
func (m *Mask) Rect(x0, y0, x1, y1 float64) {
	if x1 <= x0 || y1 <= y0 {
		return
	}
	b := m.span(x0, y0, x1, y1)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		ky := overlap(float64(y), y0, y1)
		for x := b.Min.X; x < b.Max.X; x++ {
			m.put(y*m.W+x, ky*overlap(float64(x), x0, x1))
		}
	}
}

// overlap is how much of the pixel p..p+1 lies in a..b.
func overlap(p, a, b float64) float32 {
	return float32(max(0, min(p+1, b)-max(p, a)))
}

// Poly adds the polygon through pts, closed.
func (m *Mask) Poly(pts [][2]float64) {
	if len(pts) < 3 {
		return
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range pts {
		x0, y0, x1, y1 = min(x0, p[0]), min(y0, p[1]), max(x1, p[0]), max(y1, p[1])
	}
	m.fill(x0, y0, x1, y1, func(z *vector.Rasterizer, at func(x, y float64) (float32, float32)) {
		z.MoveTo(at(pts[0][0], pts[0][1]))
		for _, p := range pts[1:] {
			z.LineTo(at(p[0], p[1]))
		}
		z.ClosePath()
	})
}

// fill rasterises an outline that stays inside x0, y0, x1, y1 into the mask.
// build draws the outline; at turns canvas units into the rasteriser's.
func (m *Mask) fill(x0, y0, x1, y1 float64, build func(z *vector.Rasterizer, at func(x, y float64) (float32, float32))) {
	b := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1))+1, int(math.Ceil(y1))+1).
		Intersect(image.Rect(0, 0, m.W, m.H))
	if b.Empty() {
		return
	}
	w, h := b.Dx(), b.Dy()
	if cap(m.pix) < w*h {
		m.pix = make([]byte, w*h)
	}
	a := &image.Alpha{Pix: m.pix[:w*h], Stride: w, Rect: image.Rect(0, 0, w, h)}
	clear(a.Pix)
	z := &m.z
	z.Reset(w, h)
	z.DrawOp = draw.Src
	ox, oy := float64(b.Min.X), float64(b.Min.Y)
	build(z, func(x, y float64) (float32, float32) { return float32(x - ox), float32(y - oy) })
	z.Draw(a, a.Rect, image.Opaque, image.Point{})
	m.box = m.box.Union(b)
	for y := range h {
		row := (b.Min.Y+y)*m.W + b.Min.X
		for x, v := range a.Pix[y*w : y*w+w] {
			if v != 0 {
				m.put(row+x, float32(v)/255)
			}
		}
	}
}
