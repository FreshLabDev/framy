// SPDX-License-Identifier: Apache-2.0
package media

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"strconv"
	"strings"
)

// An SVG is a result framy keeps as it came: ffmpeg does not read one, and
// a person gets the file itself. Only its root is read, for its size, which
// is what the site lays the picture out by.

const (
	// svgHead bounds what is read for the root: a declaration, comments
	// and a doctype may come before it.
	svgHead = 64 << 10
	// maxSVGSide bounds a size an SVG declares. A vector picture has no
	// pixels, and a larger number tells a person nothing.
	maxSVGSide = 1 << 16
)

// cssUnits are the absolute CSS units in pixels; a length in any other
// (%, em, vw) depends on where the picture is shown.
var cssUnits = map[string]float64{"": 1, "px": 1, "in": 96, "cm": 96 / 2.54, "mm": 96 / 25.4, "q": 96 / 101.6, "pt": 96.0 / 72, "pc": 16}

// SVGSize reads the root element of r. ok tells it is an SVG document:
// its root is <svg>. width and height are the size it declares in CSS
// pixels: its width and height when both are absolute lengths, one of them
// with the shape of its viewBox, or the viewBox alone; zero when it
// declares none. Nothing past the root's start tag is read, and no entity
// or outside reference is followed.
func SVGSize(r io.Reader) (width, height int, ok bool) {
	d := xml.NewDecoder(io.LimitReader(r, svgHead))
	// The root's name and size are ASCII in any charset an SVG declares.
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := d.RawToken()
		if err != nil {
			return 0, 0, false
		}
		switch t := tok.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(bytes.TrimPrefix(t, []byte("\uFEFF")))) > 0 {
				return 0, 0, false
			}
		case xml.StartElement:
			if t.Name.Local != "svg" {
				return 0, 0, false
			}
			width, height = svgSize(t.Attr)
			return width, height, true
		case xml.EndElement:
			return 0, 0, false
		}
	}
}

func svgSize(attrs []xml.Attr) (int, int) {
	var w, h, vbw, vbh float64
	for _, a := range attrs {
		if a.Name.Space != "" {
			continue
		}
		switch a.Name.Local {
		case "width":
			w = cssLength(a.Value)
		case "height":
			h = cssLength(a.Value)
		case "viewBox":
			vbw, vbh = viewBox(a.Value)
		}
	}
	switch {
	case w > 0 && h > 0:
	case w > 0 && vbw > 0:
		h = w * vbh / vbw
	case h > 0 && vbh > 0:
		w = h * vbw / vbh
	case vbw > 0:
		w, h = vbw, vbh
	default:
		return 0, 0
	}
	iw, ih := int(math.Max(1, math.Round(w))), int(math.Max(1, math.Round(h)))
	if iw > maxSVGSide || ih > maxSVGSide {
		return 0, 0
	}
	return iw, ih
}

// cssLength is an absolute length in pixels, or 0.
func cssLength(s string) float64 {
	s = strings.ToLower(strings.TrimSpace(s))
	num := strings.TrimRight(s, "abcdefghijklmnopqrstuvwxyz")
	per, known := cssUnits[s[len(num):]]
	v, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if !known || err != nil || !(v > 0) || math.IsInf(v, 0) {
		return 0
	}
	return v * per
}

// viewBox is the width and height of "min-x min-y width height", or 0, 0.
func viewBox(s string) (float64, float64) {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(f) != 4 {
		return 0, 0
	}
	w, err1 := strconv.ParseFloat(f[2], 64)
	h, err2 := strconv.ParseFloat(f[3], 64)
	if err1 != nil || err2 != nil || !(w > 0) || !(h > 0) || math.IsInf(w, 0) || math.IsInf(h, 0) {
		return 0, 0
	}
	return w, h
}
