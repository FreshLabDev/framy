// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// textBox is where the card's line may stand: right of the viewfinder, in
// the width of cardTextWidth, inside the frame's height.
var textBox = image.Rect(cardTextX, frameY0, cardTextX+cardTextWidth, frameY1)

// inkRightOf is the box of the pixels from column left on that are not the
// field.
func inkRightOf(c *Canvas, left int) image.Rectangle {
	var box image.Rectangle
	for y := range c.H {
		for x := left; x < c.W; x++ {
			if !near(rgb(c, x, y), rgbOf(Field), 24) {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

// The line stays in its box and in four lines at most, whatever its length, and a
// line that is too long ends in an ellipsis.
func TestCardKeepsItsLineInsideTheBox(t *testing.T) {
	long := strings.Repeat("Der Anbieter hat das Bild abgelehnt, weil es gegen seine Regeln verstößt. ", 6)
	for _, c := range []struct {
		name, text, lang string
		cut              bool
	}{
		{"short", "No credits left.", "en", false},
		{"two lines", "The model refused this picture. Try other words.", "en", false},
		{"russian", "Модель отказалась: в запросе нашлось то, что запрещено правилами. Попробуйте другие слова.", "ru", false},
		{"too long", long, "de", true},
		{"one long word", strings.Repeat("Donaudampfschifffahrtsgesellschaft", 3), "de", true},
		{"line breaks", "No credits\n\nleft.\t Add some.", "en", false},
	} {
		k := NewCard(c.text, c.lang)
		if !k.TextDrawn() || len(k.lines) == 0 || len(k.lines) > cardMaxLines {
			t.Errorf("%s: %d lines, drawn %v", c.name, len(k.lines), k.TextDrawn())
			continue
		}
		if cut := strings.HasSuffix(k.lines[len(k.lines)-1], "…"); cut != c.cut {
			t.Errorf("%s: ends in an ellipsis is %v, want %v: %q", c.name, cut, c.cut, k.lines)
		}
		for _, l := range k.lines {
			if strings.ContainsAny(l, "\n\t") || strings.Contains(l, "  ") {
				t.Errorf("%s: unclosed spaces in %q", c.name, l)
			}
		}
		if box := inkRightOf(frameAt(k, 0), cardMarkX+160); !box.In(textBox) {
			t.Errorf("%s: the line is at %v, outside %v", c.name, box, textBox)
		}
	}
}

// A line the faces cannot spell is left out and the card says so: the bot's
// caption carries the same words, and a row of empty boxes helps nobody.
func TestCardLeavesOutWhatTheFacesCannotSpell(t *testing.T) {
	bare := frameAt(NewCard("", "en"), 0)
	for lang, text := range map[string]string{
		"ja": "モデルがこの画像を拒否しました。",
		"zh": "模型拒绝了这张图片。",
		"ar": "رفض النموذج هذه الصورة.",
		"en": "A picture with an emoji 🙂",
	} {
		k := NewCard(text, lang)
		if k.TextDrawn() {
			t.Errorf("%s: drawn", lang)
		}
		if !bytes.Equal(frameAt(k, 0).Image().Pix, bare.Image().Pix) {
			t.Errorf("%s: the card with no line is not the card without text", lang)
		}
	}
	if NewCard("   \n ", "en").TextDrawn() {
		t.Error("a blank line is drawn")
	}
}

// Sofia Sans draws Cyrillic in its Bulgarian forms unless told the language,
// so the card must pass the person's language on to the shaper.
func TestCardSetsTheLanguageOfItsLetters(t *testing.T) {
	text := "кино и дым"
	ru, bg := frameAt(NewCard(text, "ru"), 0), frameAt(NewCard(text, "bg"), 0)
	if bytes.Equal(ru.Image().Pix, bg.Image().Pix) {
		t.Error("Russian and Bulgarian letters look the same: the language is not set")
	}
}

// The font has local forms for Russian only, so Ukrainian and Belarusian
// must come out in the Russian ones and not in the Bulgarian default.
func TestUkrainianAndBelarusianAreNotDrawnInBulgarianForms(t *testing.T) {
	text := "вдтш"
	ru := Body.Layout(text, 100, ShapingLang("ru")).Width
	bg := Body.Layout(text, 100, ShapingLang("bg")).Width
	if ru == bg {
		t.Fatal("the Russian and Bulgarian forms are as wide: the probe tells nothing")
	}
	for _, lang := range []string{"uk", "be"} {
		if w := Body.Layout(text, 100, ShapingLang(lang)).Width; w != ru {
			t.Errorf("%s: %.1f wide, Russian forms are %.1f", lang, w, ru)
		}
	}
}

// Every letter that framy's interface uses in the thirteen languages the
// faces cover has a glyph in them; the other three are known not to.
func TestFacesCoverTheLanguagesTheyShouldAndOnlyThose(t *testing.T) {
	uncovered := map[string]bool{"zh": true, "ja": true, "ar": true}
	seen := map[string]int{}
	for _, lang := range i18n.Codes() {
		var letters strings.Builder
		for _, key := range i18n.Keys() {
			for _, r := range i18n.T(lang, key) {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					letters.WriteRune(r)
				}
			}
		}
		seen[lang] = letters.Len()
		for _, r := range letters.String() {
			if !uncovered[lang] && !Covers(string(r)) {
				t.Errorf("%s: no glyph for %q (U+%04X)", lang, r, r)
			}
		}
		if uncovered[lang] && Covers(letters.String()) {
			t.Errorf("%s: covered; the faces are said to lack it, and the card's note and the design say so", lang)
		}
	}
	if len(seen) != 16 {
		t.Errorf("%d languages", len(seen))
	}
	// And the marks a line of the bot's may use.
	if !Covers("«Ёё Її Єє Ўў Ґґ» ¿¡ œ ß ç ñ ł ż ğ ı İ ş ě ř ů å ä ö l·l … – : ; ! ? ( ) „ “ ” ’ 0123456789  ") {
		t.Error("a letter or a mark of the thirteen languages has no glyph")
	}
}

func TestErrorCardIsAPNGKeptByItsKey(t *testing.T) {
	r := &Renderer{Cache: t.TempDir()}
	ctx := context.Background()
	m, err := r.ErrorCard(ctx, "The model refused this picture. Try other words.", "en")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != m.Width || b.Dy() != m.Height || m.Width != Width || m.Height != Height {
		t.Errorf("%v, said to be %d×%d", b, m.Width, m.Height)
	}
	st, _ := os.Stat(m.Path)
	if m.ContentType != "image/png" || m.Duration != 0 || !m.TextDrawn || m.Key == "" || m.Bytes != st.Size() {
		t.Errorf("%+v", m)
	}
	// A photo for Telegram: 10 MB, sides adding up to 10000, ratio under 20.
	if m.Bytes > MaxCardBytes || m.Width+m.Height > 10000 {
		t.Errorf("%d bytes, %d×%d", m.Bytes, m.Width, m.Height)
	}
	// The same line is drawn once, and a line in another language, or another
	// line, is another card.
	again, err := r.ErrorCard(ctx, "The model refused this picture.  Try other words.", "en")
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(again.Path)
	if again.Path != m.Path || !os.SameFile(st, st2) {
		t.Error("the same line was drawn twice")
	}
	other, _ := r.ErrorCard(ctx, "The model refused this picture. Try other words.", "uk")
	if other.Key == m.Key || other.Path == m.Path {
		t.Error("the line in another language is the same card")
	}
	// A line that cannot be drawn is a card without one, and says so.
	ja, err := r.ErrorCard(ctx, "モデルがこの画像を拒否しました。", "ja")
	if err != nil || ja.TextDrawn {
		t.Errorf("ja: %v, %+v", err, ja)
	}
	left, _ := filepath.Glob(filepath.Join(r.Cache, "*.part"))
	if len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if _, err := (&Renderer{Cache: t.TempDir()}).ErrorCard(canceled(), "x", "en"); err == nil {
		t.Error("a cancelled call drew a card")
	}
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
