// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"bytes"
	_ "embed"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Sofia Sans and Sofia Sans Extra Condensed, the family's faces, as variable
// fonts under the SIL Open Font License (fonts/OFL.txt). They cover Latin,
// Latin Extended and Cyrillic: thirteen of the sixteen languages. Chinese,
// Japanese and Arabic are not in them, and Covers says so.
var (
	//go:embed fonts/SofiaSans.ttf
	sofiaSans []byte
	//go:embed fonts/SofiaSansExtraCondensed.ttf
	sofiaSansExtraCondensed []byte
)

// The faces of the pictures: the display face of the name and the figures,
// and the body face of a line of text.
var (
	Display = mustFont(sofiaSansExtraCondensed, 700)
	Body    = mustFont(sofiaSans, 500)
)

// maxRuns bounds the shaped lines a face keeps, so lines of every language and
// size over the months of a process do not pile up.
const maxRuns = 2048

// Font is one face at one weight. It shapes with HarfBuzz rules, so the
// language picks the local forms: Sofia Sans draws Bulgarian Cyrillic unless
// told the text is Russian (see ShapingLang).
type Font struct {
	mu     sync.Mutex
	face   *font.Face
	shaper shaping.HarfbuzzShaper
	upem   float64
	runs   map[runKey]*Run
}

type runKey struct {
	s    string
	size float64
	lang language.Language
}

func mustFont(ttf []byte, weight float32) *Font {
	face, err := font.ParseTTF(bytes.NewReader(ttf))
	if err != nil {
		panic("anim: " + err.Error())
	}
	face.SetVariations([]font.Variation{{Tag: ot.MustNewTag("wght"), Value: weight}})
	return &Font{face: face, upem: float64(face.Upem()), runs: map[runKey]*Run{}}
}

// ShapingLang is the language the faces are told for text in the language
// code. Sofia Sans has local forms for Russian only: told Ukrainian or
// Belarusian, it keeps its Bulgarian ones, a в like a ϐ and a т like an m.
// Those two are shaped as Russian, whose forms are theirs too.
func ShapingLang(code string) language.Language {
	switch code {
	case "uk", "be":
		code = "ru"
	}
	return language.NewLanguage(code)
}

// Run is a shaped line: its outline in pixels with the pen at (0, 0) on the
// baseline, y down, and how far the pen moved.
type Run struct {
	segs        []glyphSeg
	Width       float64
	left, right float64 // the outline's extent beside the pen
	top, bottom float64 // and above and below the baseline
}

type glyphSeg struct {
	op  ot.SegmentOp
	pts [3][2]float64
}

// Layout shapes s at size pixels. Runs are cached, so a frame that draws the
// same words as the last costs only their rasterising.
func (f *Font) Layout(s string, size float64, lang language.Language) *Run {
	key := runKey{s, size, lang}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.runs[key]; ok {
		return r
	}
	if len(f.runs) >= maxRuns {
		clear(f.runs)
	}
	text := []rune(s)
	out := f.shaper.Shape(shaping.Input{
		Text: text, RunStart: 0, RunEnd: len(text), Direction: di.DirectionLTR,
		Face: f.face, Size: fixed.Int26_6(math.Round(size * 64)), Script: scriptOf(text), Language: lang,
	})
	k := size / f.upem
	r := &Run{left: math.Inf(1), right: math.Inf(-1), top: math.Inf(1), bottom: math.Inf(-1)}
	pen := 0.0
	for _, g := range out.Glyphs {
		ox, oy := pen+fx(g.XOffset), -fx(g.YOffset)
		if outline, ok := f.face.GlyphData(g.GlyphID).(font.GlyphOutline); ok {
			for _, s := range outline.Segments {
				var gs glyphSeg
				gs.op = s.Op
				for i, p := range s.ArgsSlice() {
					gs.pts[i] = [2]float64{ox + float64(p.X)*k, oy - float64(p.Y)*k}
					r.left, r.right = min(r.left, gs.pts[i][0]), max(r.right, gs.pts[i][0])
					r.top, r.bottom = min(r.top, gs.pts[i][1]), max(r.bottom, gs.pts[i][1])
				}
				r.segs = append(r.segs, gs)
			}
		}
		pen += fx(g.Advance)
	}
	if len(r.segs) == 0 {
		r.left, r.right, r.top, r.bottom = 0, 0, 0, 0
	}
	r.Width = pen
	f.runs[key] = r
	return r
}

func fx(v fixed.Int26_6) float64 { return float64(v) / 64 }

// scriptOf is the script of the first letter that has one: the digits and
// signs of "20:00" go with the letters around them.
func scriptOf(text []rune) language.Script {
	for _, r := range text {
		if s := language.LookupScript(r); s != language.Common && s != language.Inherited && s != language.Unknown {
			return s
		}
	}
	return language.Latin
}

// Align says which end of a line its x is.
type Align int

const (
	Left Align = iota
	Center
	Right
)

// Text draws s with its baseline at y and x at the given end, and returns its
// width.
func (c *Canvas) Text(f *Font, size float64, s string, x, y float64, align Align, col RGB, alpha float64) float64 {
	r := f.Layout(s, size, c.Lang)
	switch align {
	case Center:
		x -= r.Width / 2
	case Right:
		x -= r.Width
	}
	c.mask.Run(r, x, y)
	c.paint(c.mask, col, alpha)
	return r.Width
}

// Figure draws s, a few digits, centred on cx, cy: on the middle of the
// figures themselves, not of the line's em box.
func (c *Canvas) Figure(f *Font, size float64, s string, cx, cy float64, col RGB, alpha float64) {
	r := f.Layout(s, size, c.Lang)
	c.mask.Run(r, cx-r.Width/2, cy-(r.top+r.bottom)/2)
	c.paint(c.mask, col, alpha)
}

// Measure is the width of s.
func (c *Canvas) Measure(f *Font, size float64, s string) float64 {
	return f.Layout(s, size, c.Lang).Width
}

// Run adds a shaped line to the mask with the pen at x, y.
func (m *Mask) Run(r *Run, x, y float64) {
	if len(r.segs) == 0 {
		return
	}
	m.fill(x+r.left, y+r.top, x+r.right, y+r.bottom, func(z *vector.Rasterizer, at func(x, y float64) (float32, float32)) {
		open := false
		pt := func(p [2]float64) (float32, float32) { return at(x+p[0], y+p[1]) }
		for _, g := range r.segs {
			switch g.op {
			case ot.SegmentOpMoveTo:
				if open {
					z.ClosePath()
				}
				z.MoveTo(pt(g.pts[0]))
				open = true
			case ot.SegmentOpLineTo:
				z.LineTo(pt(g.pts[0]))
			case ot.SegmentOpQuadTo:
				bx, by := pt(g.pts[0])
				cx, cy := pt(g.pts[1])
				z.QuadTo(bx, by, cx, cy)
			case ot.SegmentOpCubeTo:
				bx, by := pt(g.pts[0])
				cx, cy := pt(g.pts[1])
				dx, dy := pt(g.pts[2])
				z.CubeTo(bx, by, cx, cy, dx, dy)
			}
		}
		if open {
			z.ClosePath()
		}
	})
}

// Covers tells whether the body face has a glyph for every letter of s.
// A line in Chinese, Japanese or Arabic is not drawn: the faces would draw an
// empty box for each letter.
func Covers(s string) bool {
	Body.mu.Lock()
	defer Body.mu.Unlock()
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if _, ok := Body.face.NominalGlyph(r); !ok {
			return false
		}
	}
	return true
}

// lines breaks s at its spaces into lines no wider than width at size, as
// many words to a line as fit. A word wider than the width stands alone, so
// the longest line may still be wider.
func (c *Canvas) lines(f *Font, size float64, s string, width float64) []string {
	var out []string
	var line string
	for _, w := range strings.Fields(s) {
		next := w
		if line != "" {
			next = line + " " + w
		}
		if line != "" && c.Measure(f, size, next) > width {
			out = append(out, line)
			next = w
		}
		line = next
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
