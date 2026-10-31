// SPDX-License-Identifier: Apache-2.0
package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/webp"
)

// thumbConverter is converter for an ffmpeg that also writes WebP.
func thumbConverter(t *testing.T) *Converter {
	t.Helper()
	c := converter(t)
	if err := c.CheckThumb(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot write thumbnails here: %v", err)
	}
	return c
}

// thumb writes data as a file and its thumbnail next to it, and decodes the
// thumbnail.
func thumb(t *testing.T, c *Converter, data []byte) (image.Image, int, int, error) {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, In), filepath.Join(dir, "thumb.webp")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	w, h, err := c.Thumb(context.Background(), in, out)
	if err != nil {
		return nil, 0, 0, err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	pic, err := webp.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the thumbnail is not a WebP: %v", err)
	}
	if b := pic.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("Thumb says %d×%d, the picture is %v", w, h, b)
	}
	return pic, w, h, nil
}

func pngOf(t *testing.T, w, h int, at func(x, y int) color.NRGBA) []byte {
	t.Helper()
	pic := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			pic.SetNRGBA(x, y, at(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pic); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAThumbIsASmallWebP(t *testing.T) {
	c := thumbConverter(t)
	halves := func(x, _ int) color.NRGBA {
		if x < 600 {
			return color.NRGBA{R: 0xcc, G: 0x33, B: 0x33, A: 255}
		}
		return color.NRGBA{R: 0x33, G: 0x4d, B: 0xcc, A: 255}
	}
	pic, w, h, err := thumb(t, c, pngOf(t, 1200, 600, halves))
	if err != nil {
		t.Fatal(err)
	}
	if w != ThumbLongSide || h != ThumbLongSide/2 {
		t.Errorf("%d×%d, want %d×%d", w, h, ThumbLongSide, ThumbLongSide/2)
	}
	if !near(pic.At(w/4, h/2), red) || !near(pic.At(3*w/4, h/2), blue) {
		t.Errorf("the colours moved: %s and %s", hex(pic.At(w/4, h/2)), hex(pic.At(3*w/4, h/2)))
	}

	// A picture smaller than a thumbnail keeps its size, and EXIF still
	// turns it upright.
	_, w, h, err = thumb(t, c, fixture(t, "exif.jpg"))
	if err != nil || w != 300 || h != 400 {
		t.Errorf("exif.jpg: %d×%d %v, want 300×400", w, h, err)
	}
}

func TestAThumbKeepsTheAlpha(t *testing.T) {
	c := thumbConverter(t)
	pic, w, h, err := thumb(t, c, pngOf(t, 300, 200, func(x, _ int) color.NRGBA {
		if x < 150 {
			return color.NRGBA{R: 200, G: 40, B: 40, A: 255}
		}
		return color.NRGBA{}
	}))
	if err != nil {
		t.Fatal(err)
	}
	left := color.NRGBAModel.Convert(pic.At(w/4, h/2)).(color.NRGBA)
	right := color.NRGBAModel.Convert(pic.At(3*w/4, h/2)).(color.NRGBA)
	if left.A < 250 || right.A > 5 {
		t.Errorf("alpha lost: left %v, right %v", left, right)
	}
}

func TestWhatIsNotAPictureHasNoThumb(t *testing.T) {
	c := thumbConverter(t)
	if _, _, _, err := thumb(t, c, []byte("not a picture at all")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("%v, want ErrUnsupported", err)
	}
}
