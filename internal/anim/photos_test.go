// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// scenery makes a landscape: a sky, a sun, three ranges of hills and a
// grain, the way a photo has one. seed picks the colours and the hills, so
// the photos of one album differ.
func scenery(w, h, seed int, grain float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	hue := float64(seed) * 0.37
	pal := func(k, light float64) (float64, float64, float64) {
		return light * (0.55 + 0.45*math.Sin(hue+k)),
			light * (0.55 + 0.45*math.Sin(hue+k+2.1)),
			light * (0.55 + 0.45*math.Sin(hue+k+4.2))
	}
	rnd := uint32(seed*7919 + 17)
	noise := func() float64 {
		rnd = rnd*1664525 + 1013904223
		return float64(rnd>>16)/65535 - 0.5
	}
	sunX, sunY := float64(w)*(0.2+0.6*math.Mod(float64(seed)*0.31, 1)), float64(h)*0.32
	for y := range h {
		fy := float64(y) / float64(h)
		for x := range w {
			fx := float64(x) / float64(w)
			r, g, b := pal(0, 0.55+0.45*(1-fy))
			if d := math.Hypot(float64(x)-sunX, float64(y)-sunY) / float64(h); d < 0.09 {
				r, g, b = 1, 0.95, 0.8
			} else if d < 0.2 {
				k := (0.2 - d) / 0.11
				r, g, b = r+(1-r)*k*0.4, g+(0.9-g)*k*0.4, b+(0.7-b)*k*0.4
			}
			for layer := range 3 {
				ridge := 0.55 + 0.12*float64(layer) +
					0.05*math.Sin(fx*(5+float64(layer)*3)+float64(seed)+float64(layer)*2) +
					0.025*math.Sin(fx*(17+float64(layer)*5)+float64(seed)*1.7)
				if fy > ridge {
					r, g, b = pal(1+float64(layer)*1.3, 0.62-0.17*float64(layer))
				}
			}
			n := noise() * grain
			img.SetRGBA(x, y, color.RGBA{clamp8(r + n), clamp8(g + n), clamp8(b + n), 255})
		}
	}
	return img
}

// texture is value noise of seven octaves: the detail of a real photo, which
// the smooth hills of scenery do not have, and so the picture that costs the
// encoder the most.
func texture(w, h, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	hash := func(x, y, o int) float64 {
		n := uint32(x)*374761393 + uint32(y)*668265263 + uint32(o)*2147483647 + uint32(seed)*40503
		n = (n ^ n>>13) * 1274126177
		return float64(n^n>>16) / 4294967296
	}
	value := func(fx, fy float64, o int) float64 {
		x, y := int(math.Floor(fx)), int(math.Floor(fy))
		tx, ty := fx-float64(x), fy-float64(y)
		tx, ty = tx*tx*(3-2*tx), ty*ty*(3-2*ty)
		a := hash(x, y, o) + (hash(x+1, y, o)-hash(x, y, o))*tx
		b := hash(x, y+1, o) + (hash(x+1, y+1, o)-hash(x, y+1, o))*tx
		return a + (b-a)*ty
	}
	for y := range h {
		for x := range w {
			v, amp, f := 0.0, 0.5, 4.0/float64(w)
			for o := range 7 {
				v += amp * value(float64(x)*f, float64(y)*f, o)
				amp /= 2
				f *= 2
			}
			g := min(1, max(0, v*1.3))
			img.SetRGBA(x, y, color.RGBA{clamp8(g), clamp8(math.Pow(g, 1.3)), clamp8(math.Pow(g, 1.8)), 255})
		}
	}
	return img
}

func clamp8(v float64) uint8 { return uint8(math.Round(min(1, max(0, v)) * 255)) }

// solid is a picture of one colour.
func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, b, a := c.RGBA()
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(r>>8), uint8(g>>8), uint8(b>>8), uint8(a>>8)
	}
	return img
}

// writeJPEG writes img the way internal/media writes a photo.
func writeJPEG(t testing.TB, path string, img image.Image) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePNG(t testing.TB, path string, img image.Image) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// photoSizes are the shapes phones and cameras give, in the order an album of
// ten is likely to hold them. scale shrinks them for tests that do not time
// the decoding.
var photoSizes = [][2]int{
	{2048, 1536}, {1536, 2048}, {2048, 1152}, {1536, 2048}, {2048, 2048},
	{2048, 1536}, {2048, 1536}, {1152, 2048}, {2048, 1536}, {1536, 2048},
}

// album writes n photos to dir and returns their paths: landscapes and
// textures in turn, in the shapes of photoSizes divided by shrink.
func album(t testing.TB, dir string, n, shrink int) []string {
	t.Helper()
	var paths []string
	for i := range n {
		w, h := photoSizes[i%len(photoSizes)][0]/shrink, photoSizes[i%len(photoSizes)][1]/shrink
		var img image.Image = scenery(w, h, i+1, 0.06)
		if i%2 == 1 {
			img = texture(w, h, i+1)
		}
		paths = append(paths, writeJPEG(t, filepath.Join(dir, "photo"+string(rune('a'+i))+".jpg"), img))
	}
	return paths
}
