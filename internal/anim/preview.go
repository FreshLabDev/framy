// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"fmt"
	"image"
	"strconv"
	"strings"

	"github.com/go-text/typesetting/language"
)

// The preview card: the viewfinder on the left, the model and the run's
// settings on the right.
const (
	pvFrameX1 = 600 // the viewfinder's right edge; it starts where every frame does
	pvColX    = 664 // where the right column starts
	pvColX1   = Width - 48
	pvColW    = pvColX1 - pvColX
)

// Preview is the card a run shows before Generate: what it will make and
// with what, readable without the caption. The viewfinder holds the photos
// the run takes, numbered, or with none a field of dots in the shape of the
// result. Beside it are the maker and the model, the settings as chips and
// what the run costs. The prompt is not on it: the caption carries it, in
// any script and at any length.
//
// The words are drawn only where the faces have the letters; the chips, the
// numbers and the meter of the free allowance need none.
type Preview struct {
	Lang   string
	Maker  string   // "Microsoft AI"
	Model  string   // "MAI-Image-2.6 Flash"
	Aspect string   // the result's shape, "16:9"; empty when the model decides
	Chips  []string // the other settings, short: "1K", "×1"
	// Free is a run on the free allowance: Left of Limit are left, this run
	// not yet taken. Otherwise Price is what the person's key pays, in
	// dollars, and Local the same in their currency, if another.
	Free         bool
	Left, Limit  int
	Price, Local string
	// LocalCode is Local with the currency's code for its sign ("≈ 5,63
	// UAH"), for the signs the faces lack: ₴ and ₽ among them.
	LocalCode string
	// Label is the line over the price: "free" or "your OpenRouter key", in
	// Lang.
	Label string

	lang  language.Language
	pics  []*Pic
	rects []image.Rectangle
	badge float64
	name  []string // the model's name in lines
	size  float64  // and their size
}

// The box inside the viewfinder the photos or the dots go in.
var pvBox = image.Rect(frameX0+frameInset, frameY0+frameInset, pvFrameX1-frameInset, frameY1-frameInset)

// NewPreview lays out the card, reading the photos at paths (none, or up to
// MaxPhotos).
func NewPreview(ctx context.Context, p Preview, paths []string) (*Preview, error) {
	if len(paths) > MaxPhotos {
		return nil, ErrNoPhotos
	}
	p.lang = ShapingLang(p.Lang)
	if !Covers(p.Label) {
		p.Label = ""
	}
	if !Covers(p.Local) {
		p.Local = p.LocalCode
	}
	if !Covers(p.Price) {
		// "price unknown" in a script the faces lack: the caption says it.
		p.Price, p.Local = "", ""
	}
	if len(paths) > 0 {
		aspects := make([]float64, len(paths))
		for i, path := range paths {
			w, h, err := photoSize(path)
			if err != nil {
				return nil, fmt.Errorf("photo %d: %w", i+1, err)
			}
			aspects[i] = min(2, max(0.5, float64(w)/float64(h)))
		}
		p.rects = layout(aspects, pvBox, gap)
		p.badge = min(56, badgeSize(p.rects))
		p.pics = make([]*Pic, len(paths))
		for i, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			pic, err := loadPic(path, p.rects[i].Dx(), p.rects[i].Dy())
			if err != nil {
				return nil, fmt.Errorf("photo %d: %w", i+1, err)
			}
			p.pics[i] = pic
		}
	}
	c := &Canvas{Lang: p.lang}
	p.name, p.size = c.fitName(p.Model)
	return &p, nil
}

// fitName sets the model's name in at most two lines, as large as fits the
// column. A name too long for two lines at the smallest size ends in an
// ellipsis.
func (c *Canvas) fitName(name string) ([]string, float64) {
	name = strings.Join(strings.Fields(name), " ")
	const smallest = 64.0
	for size := 132.0; size > smallest; size -= 4 {
		lines := c.lines(Display, size, name, pvColW)
		fits := len(lines) <= 2
		for _, l := range lines {
			fits = fits && c.Measure(Display, size, l) <= pvColW
		}
		if fits {
			return lines, size
		}
	}
	lines := c.lines(Display, smallest, name, pvColW)
	if len(lines) > 2 {
		lines = []string{lines[0], strings.Join(lines[1:], " ")}
	}
	for i, l := range lines {
		r := []rune(l)
		for c.Measure(Display, smallest, l) > pvColW && len(r) > 1 {
			r = r[:len(r)-1]
			l = strings.TrimRight(string(r), " ,;:.-(") + "…"
		}
		lines[i] = l
	}
	return lines, smallest
}

func (p *Preview) Size() (int, int)  { return Width, Height }
func (p *Preview) Duration() float64 { return 0 }

func (p *Preview) Draw(c *Canvas, _ float64) {
	c.Lang = p.lang
	c.Fill(Field)
	c.header()
	c.brackets(frameX0, frameY0, pvFrameX1, frameY1, 64, white, 1)
	if len(p.pics) > 0 {
		p.drawPhotos(c)
	} else {
		p.drawShape(c)
	}
	p.drawColumn(c)
}

func (p *Preview) drawPhotos(c *Canvas) {
	all := p.rects[0]
	for _, r := range p.rects {
		all = all.Union(r)
	}
	c.Shadow(float64(all.Min.X)+2, float64(all.Min.Y)+8, float64(all.Max.X)-2, float64(all.Max.Y)+8, 8, shade, 0.6)
	for i, r := range p.rects {
		c.Photo(p.pics[i], float64(r.Min.X), float64(r.Min.Y), Rest, 1)
		c.number(i+1, float64(r.Min.X), float64(r.Min.Y), p.badge)
	}
}

// drawShape is the result to come: dots in its shape, with the shape's name.
func (p *Preview) drawShape(c *Canvas) {
	w, h := aspectOf(p.Aspect)
	bw, bh := float64(pvBox.Dx()), float64(pvBox.Dy())
	k := min(bw/w, bh/h)
	w, h = w*k, h*k
	x0 := float64(pvBox.Min.X) + (bw-w)/2
	y0 := float64(pvBox.Min.Y) + (bh-h)/2
	const step = 20.0
	nx, ny := int(w/step), int(h/step)
	ox, oy := x0+(w-float64(nx-1)*step)/2, y0+(h-float64(ny-1)*step)/2
	for j := range ny {
		for i := range nx {
			c.mask.Disc(ox+float64(i)*step, oy+float64(j)*step, 2.4)
		}
	}
	c.paint(c.mask, haze, 0.9)
	if p.Aspect != "" {
		cx, cy := x0+w/2, y0+h/2
		tw := c.Measure(Display, 112, p.Aspect)
		c.Rect(cx-tw/2-28, cy-62, cx+tw/2+28, cy+62, Field, 1)
		c.Figure(Display, 112, p.Aspect, cx, cy, white, 1)
	}
}

// aspectOf is the shape of "16:9" as a width and a height; a square for none.
func aspectOf(s string) (float64, float64) {
	a, b, ok := strings.Cut(s, ":")
	w, err1 := strconv.ParseFloat(a, 64)
	h, err2 := strconv.ParseFloat(b, 64)
	if !ok || err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 1, 1
	}
	// A strip as thin as 1:8 would leave too few dots to read.
	r := min(2.4, max(1/2.4, w/h))
	return r, 1
}

// The right column: the maker, the name and the chips centred between the
// viewfinder's top and colTop1, and the price at the bottom.
const (
	colTop0, colTop1 = frameY0, 536
	makerSize        = 34.0
	makerGap         = 26.0 // from the maker's baseline to the name's caps
	chipGap          = 36.0 // from the name's baseline to the chips
	chipH            = 64.0
	chipSize         = 44.0
	chipPad          = 18.0
)

func (p *Preview) drawColumn(c *Canvas) {
	x := float64(pvColX)
	chips := p.chips()
	maker := Covers(p.Maker) && p.Maker != ""
	lead, caps := p.size*0.92, p.size*0.72
	h := caps + lead*float64(len(p.name)-1)
	if maker {
		h += makerSize*0.7 + makerGap
	}
	if len(chips) > 0 {
		h += chipGap + chipH
	}
	y := colTop0 + (colTop1-colTop0-h)/2
	if maker {
		y += makerSize * 0.7
		c.Text(Body, makerSize, strings.ToUpper(p.Maker), x, y, Left, haze, 1)
		y += makerGap
	}
	y += caps
	for i, l := range p.name {
		if i > 0 {
			y += lead
		}
		c.Text(Display, p.size, l, x, y, Left, white, 1)
	}
	if len(chips) > 0 {
		y += chipGap
		cx := x
		for _, s := range chips {
			w := c.Measure(Display, chipSize, s) + 2*chipPad
			if cx+w > pvColX1 {
				break
			}
			c.Rect(cx, y, cx+w, y+chipH, white, 1)
			c.Figure(Display, chipSize, s, cx+w/2, y+chipH/2, Field, 1)
			cx += w + 12
		}
	}
	p.drawPrice(c, x, Height-56)
}

// chips are the settings shown. The shape is one only beside photos: with
// none, the viewfinder draws it.
func (p *Preview) chips() []string {
	var out []string
	if p.Aspect != "" && len(p.pics) > 0 {
		out = append(out, p.Aspect)
	}
	return append(out, p.Chips...)
}

// drawPrice draws what the run costs with its baseline at y.
func (p *Preview) drawPrice(c *Canvas, x, y float64) {
	if p.Label != "" {
		c.Text(Body, 32, p.Label, x, y-74, Left, haze, 1)
	}
	if p.Free {
		const side, step = 34.0, 44.0
		top := y - side
		for i := range p.Limit {
			x0 := x + float64(i)*step
			c.Rect(x0, top, x0+side, top+side, white, 1)
			if i >= p.Left {
				c.Rect(x0+5, top+5, x0+side-5, top+side-5, Field, 1)
			}
		}
		c.Text(Display, 56, fmt.Sprintf("%d/%d", p.Left, p.Limit), x+float64(p.Limit)*step+12, y, Left, white, 1)
		return
	}
	w := c.Text(Display, 84, p.Price, x, y, Left, white, 1)
	if p.Local != "" {
		c.Text(Body, 40, p.Local, x+w+20, y, Left, haze, 1)
	}
}
