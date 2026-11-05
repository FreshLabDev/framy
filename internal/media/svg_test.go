// SPDX-License-Identifier: Apache-2.0
package media

import (
	"strings"
	"testing"
)

func TestSVGSize(t *testing.T) {
	const ns = ` xmlns="http://www.w3.org/2000/svg"`
	for _, c := range []struct {
		name, doc string
		w, h      int
		ok        bool
	}{
		{"width and height", `<svg` + ns + ` width="1024" height="768"><path d="M0 0"/></svg>`, 1024, 768, true},
		{"in pixels", `<svg` + ns + ` width="1024px" height="512.4px"/>`, 1024, 512, true},
		{"in inches", `<svg` + ns + ` width="2in" height="1in"/>`, 192, 96, true},
		{"the viewBox alone", `<svg` + ns + ` viewBox="0 0 2048 1152">`, 2048, 1152, true},
		{"a viewBox with commas", `<svg` + ns + ` viewBox="-10,-10, 300,150">`, 300, 150, true},
		{"a width and the shape of the viewBox", `<svg` + ns + ` width="512" viewBox="0 0 1024 768">`, 512, 384, true},
		{"percentages fall back to the viewBox", `<svg` + ns + ` width="100%" height="100%" viewBox="0 0 640 480">`, 640, 480, true},
		{"a prolog, a comment and a doctype first", "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<!-- made by a model -->\n" +
			`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + "\n<svg" + ns + ` width="64" height="64">`, 64, 64, true},
		{"a prefixed root", `<svg:svg xmlns:svg="http://www.w3.org/2000/svg" width="10" height="20"/>`, 10, 20, true},
		{"no size", `<svg` + ns + `><circle r="4"/></svg>`, 0, 0, true},
		{"a size in em", `<svg` + ns + ` width="3em" height="2em"/>`, 0, 0, true},
		{"a size past any screen", `<svg` + ns + ` width="1e9" height="10"/>`, 0, 0, true},
		{"a broken viewBox", `<svg` + ns + ` viewBox="0 0 -5 10"/>`, 0, 0, true},
		{"HTML", `<!DOCTYPE html><html><body><svg width="5" height="5"></svg></body></html>`, 0, 0, false},
		{"text first", `hello <svg width="5" height="5"/>`, 0, 0, false},
		{"a PNG", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", 0, 0, false},
		{"nothing", ``, 0, 0, false},
	} {
		w, h, ok := SVGSize(strings.NewReader(c.doc))
		if w != c.w || h != c.h || ok != c.ok {
			t.Errorf("%s: %d×%d %v, want %d×%d %v", c.name, w, h, ok, c.w, c.h, c.ok)
		}
	}
}

// The root is all that is read: a document cut after it is still an SVG,
// and one whose root comes past what is read is not.
func TestSVGSizeReadsTheRootOnly(t *testing.T) {
	if w, h, ok := SVGSize(strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="9"><path d="M0 0 L`)); !ok || w != 8 || h != 9 {
		t.Fatalf("a cut document: %d×%d %v", w, h, ok)
	}
	late := "<!--" + strings.Repeat("x", svgHead) + "--><svg width=\"8\" height=\"9\"/>"
	if _, _, ok := SVGSize(strings.NewReader(late)); ok {
		t.Fatal("a root past the head was read")
	}
}
