// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Photos go in rows of the frame, in the order they are numbered, with the
// same gap between neighbours, inside the box, and none cut about their
// shape by more than a pixel or two (before the limit on a panorama or a
// strip).
func TestLayoutFitsEveryAlbum(t *testing.T) {
	box := albumBox
	shapes := map[string]func(i int) float64{
		"all landscape": func(int) float64 { return 4.0 / 3 },
		"all portrait":  func(int) float64 { return 3.0 / 4 },
		"all square":    func(int) float64 { return 1 },
		"mixed":         func(i int) float64 { return []float64{4.0 / 3, 3.0 / 4, 16.0 / 9, 1, 9.0 / 16}[i%5] },
		"extremes":      func(i int) float64 { return []float64{2, 0.5, 2, 0.5}[i%4] },
	}
	for name, aspect := range shapes {
		for n := 1; n <= MaxPhotos; n++ {
			aspects := make([]float64, n)
			for i := range aspects {
				aspects[i] = aspect(i)
			}
			rects := layout(aspects, box, gap)
			if len(rects) != n {
				t.Fatalf("%s, %d photos: %d places", name, n, len(rects))
			}
			for i, r := range rects {
				if !r.In(box) {
					t.Errorf("%s, %d photos: place %d, %v, is outside %v", name, n, i+1, r, box)
				}
				if float64(min(r.Dx(), r.Dy())) < badgeSize(rects) {
					t.Errorf("%s, %d photos: place %d is %d×%d, smaller than its number", name, n, i+1, r.Dx(), r.Dy())
				}
				if got := float64(r.Dx()) / float64(r.Dy()); math.Abs(got/aspects[i]-1) > 0.03 {
					t.Errorf("%s, %d photos: place %d has the shape %.3f for a photo of %.3f", name, n, i+1, got, aspects[i])
				}
				for j, o := range rects[:i] {
					if r.Overlaps(o) {
						t.Errorf("%s, %d photos: places %d and %d overlap", name, n, j+1, i+1)
					}
				}
				if i == 0 {
					continue
				}
				// Rows run left to right and then down; a neighbour in a row
				// is one gap away and as high, and a row starts one gap
				// below the last, under the first.
				p := rects[i-1]
				switch {
				case r.Min.Y == p.Min.Y:
					if r.Min.X-p.Max.X != gap || r.Dy() != p.Dy() {
						t.Errorf("%s, %d photos: places %d and %d are %d apart, %d and %d high", name, n, i, i+1, r.Min.X-p.Max.X, p.Dy(), r.Dy())
					}
				case r.Min.Y-p.Max.Y != gap || r.Min.X != rects[0].Min.X:
					t.Errorf("%s, %d photos: place %d starts the next row at %v after %v", name, n, i+1, r.Min, p)
				}
				if i == n-1 || rects[i+1].Min.Y != r.Min.Y {
					if r.Max.X != rects[n-1].Max.X {
						t.Errorf("%s, %d photos: the row of place %d ends at %d, the last row at %d", name, n, i+1, r.Max.X, rects[n-1].Max.X)
					}
				}
			}
		}
	}
}

// Four landscape photos go two by two, and a lone photo takes the whole box
// it fits.
func TestLayoutChoosesTheNaturalShape(t *testing.T) {
	box := image.Rect(0, 0, 600, 500)
	four := layout([]float64{4.0 / 3, 4.0 / 3, 4.0 / 3, 4.0 / 3}, box, gap)
	if four[0].Min.Y != four[1].Min.Y || four[2].Min.Y != four[3].Min.Y || four[0].Min.Y == four[2].Min.Y {
		t.Errorf("four landscapes are not two rows of two: %v", four)
	}
	one := layout([]float64{4.0 / 3}, box, gap)[0]
	if one.Dx() != 600 || one.Dy() < 449 || one.Dy() > 451 {
		t.Errorf("one landscape photo in a 600×500 box: %v", one)
	}
	// Five photos of mixed shapes would make a strip across the whole wide
	// frame, a third of it high; in two rows they fill more of it.
	five := layout([]float64{4.0 / 3, 3.0 / 4, 16.0 / 9, 3.0 / 4, 1}, albumBox, gap)
	if five[0].Min.Y == five[4].Min.Y {
		t.Errorf("five photos of mixed shapes are one strip: %v", five)
	}
	three := layout([]float64{4.0 / 3, 4.0 / 3, 4.0 / 3}, albumBox, gap)
	if three[0].Min.Y != three[2].Min.Y {
		t.Errorf("three landscapes are not one row in the wide frame: %v", three)
	}
}

// solidAlbum writes photos of single colours in different shapes, to tell them
// apart in the picture.
func solidAlbum(t *testing.T) (paths []string, colours []color.RGBA) {
	t.Helper()
	dir := t.TempDir()
	colours = []color.RGBA{{220, 30, 30, 255}, {30, 200, 40, 255}, {40, 60, 230, 255}, {230, 210, 30, 255}}
	sizes := [][2]int{{400, 300}, {300, 400}, {400, 400}, {400, 225}}
	for i, c := range colours {
		paths = append(paths, writeJPEG(t, filepath.Join(dir, itoa(i)+".jpg"), solid(sizes[i][0], sizes[i][1], c)))
	}
	return paths, colours
}

// The preview holds the photos in the order they came, each with its number
// at the corner. The loop starts at rest, with every photo in its place.
func TestAlbumShowsThePhotosInOrderWithTheirNumbers(t *testing.T) {
	paths, colours := solidAlbum(t)
	a, err := NewAlbum(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	hold := frameAt(a, 0)
	// What the number of each place looks like on its own: the figure i+1 in
	// its square, which the first frame must show at the same place.
	numbers := NewCanvas(Width, Height)
	numbers.Fill(Field)
	for i, r := range a.rects {
		numbers.number(i+1, float64(r.Min.X), float64(r.Min.Y), badgeSize(a.rects))
	}
	badges := make([][]byte, len(paths))
	for i, r := range a.rects {
		mid := image.Pt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2)
		if got, want := rgb(hold, mid.X, mid.Y), [3]int{int(colours[i].R), int(colours[i].G), int(colours[i].B)}; !near(got, want, 12) {
			t.Errorf("the middle of place %d is %v, want the colour of photo %d, %v", i+1, got, i+1, want)
		}
		// The number: a white square at the corner with the figure cut out of it.
		b := int(badgeSize(a.rects))
		white, ink, wrong := 0, 0, 0
		for y := r.Min.Y; y < r.Min.Y+b; y++ {
			for x := r.Min.X; x < r.Min.X+b; x++ {
				px := rgb(hold, x, y)
				if near(px, [3]int{255, 255, 255}, 8) {
					white++
				}
				if near(px, rgbOf(Field), 40) {
					ink++
				}
				badges[i] = append(badges[i], byte(px[0]))
				if want := rgb(numbers, x, y); !near(px, want, 2) {
					wrong++
				}
			}
		}
		if wrong > 0 {
			t.Errorf("place %d does not show the figure %d: %d pixels of its square differ", i+1, i+1, wrong)
		}
		if white < b*b/2 || ink < b*b/20 {
			t.Errorf("place %d has no number: %d of %d pixels white, %d in the field's colour", i+1, white, b*b, ink)
		}
	}
	for i := range badges {
		for j := range badges[:i] {
			if string(badges[i]) == string(badges[j]) {
				t.Errorf("numbers %d and %d look the same", j+1, i+1)
			}
		}
	}
}

// The album floats as one piece: halfway through the loop every photo is
// floatUp higher than its place, with its number, and none has moved apart
// from the others. On the way it never goes higher, and it stays in the
// frame.
func TestAlbumFloatsAsOnePiece(t *testing.T) {
	paths, colours := solidAlbum(t)
	a, err := NewAlbum(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	up := frameAt(a, albumDur/2)
	lift := int(floatUp)
	for i, r := range a.rects {
		want := [3]int{int(colours[i].R), int(colours[i].G), int(colours[i].B)}
		mid := (r.Min.X + r.Max.X) / 2
		for _, y := range []int{r.Min.Y - lift + r.Dy()/2, r.Max.Y - lift - 2} {
			if got := rgb(up, mid, y); !near(got, want, 12) {
				t.Errorf("photo %d is not %d pixels up: %v at %d, %d", i+1, lift, got, mid, y)
			}
		}
		// Where its bottom edge was there is no photo of its own now.
		if got := rgb(up, mid, r.Max.Y-lift/2); near(got, want, 40) {
			t.Errorf("photo %d did not rise: its colour %v is still at its bottom edge", i+1, got)
		}
		b := int(badgeSize(a.rects))
		if got := rgb(up, r.Min.X+b-3, r.Min.Y-lift+2); !near(got, [3]int{255, 255, 255}, 8) {
			t.Errorf("the number of photo %d did not rise with it: %v", i+1, got)
		}
	}
	for f := range Frames(a) + 1 {
		if h := height(float64(f) / FPS); h < 0 || h > 1 {
			t.Fatalf("frame %d: the album is at %.3f of its float", f, h)
		}
	}
	for i, r := range a.rects {
		if r.Min.Y-lift < frameY0+stripe+gap || r.Max.Y+gap > frameY1-stripe || r.Min.X < frameX0+stripe+gap || r.Max.X > frameX1-stripe-gap {
			t.Errorf("photo %d, %v, floats into the viewfinder's brackets", i+1, r)
		}
	}
}

// A photo's transparent parts show the deep colour, not black or white.
func TestAlbumShowsTransparencyAsDeep(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 400, 300))
	for y := range 300 {
		for x := 200; x < 400; x++ {
			img.SetNRGBA(x, y, color.NRGBA{20, 180, 60, 255})
		}
	}
	a, err := NewAlbum(context.Background(), []string{writePNG(t, filepath.Join(t.TempDir(), "a.png"), img)})
	if err != nil {
		t.Fatal(err)
	}
	r, c := a.rects[0], frameAt(a, 0)
	if got := rgb(c, r.Min.X+r.Dx()/4, r.Min.Y+r.Dy()/2); !near(got, rgbOf(deep), 3) {
		t.Errorf("the transparent half is %v, want the deep colour %v", got, rgbOf(deep))
	}
	if got := rgb(c, r.Min.X+3*r.Dx()/4, r.Min.Y+r.Dy()/2); !near(got, [3]int{20, 180, 60}, 3) {
		t.Errorf("the opaque half is %v", got)
	}
}

// pngOfSize is the head of a PNG that says it is w×h: enough for
// image.DecodeConfig and for nothing else.
func pngOfSize(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2
	chunk := append([]byte("IHDR"), ihdr...)
	out := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d"), chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

func TestAlbumRefusesWhatItCannotDraw(t *testing.T) {
	dir := t.TempDir()
	good := writeJPEG(t, filepath.Join(dir, "ok.jpg"), solid(40, 30, color.RGBA{9, 9, 9, 255}))
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	many := make([]string, MaxPhotos+1)
	for i := range many {
		many[i] = good
	}
	for _, c := range []struct {
		name  string
		paths []string
		want  error
		in    string
	}{
		{"none", nil, ErrNoPhotos, ""},
		{"eleven", many, ErrNoPhotos, ""},
		{"a text file", []string{good, write("t.jpg", []byte("not a picture"))}, ErrNotAPhoto, "photo 2"},
		{"a GIF", []string{write("g.gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))}, ErrNotAPhoto, "photo 1"},
		{"a decompression bomb", []string{good, good, write("b.png", pngOfSize(30000, 30000))}, ErrNotAPhoto, "photo 3"},
		{"a missing file", []string{filepath.Join(dir, "missing.jpg")}, os.ErrNotExist, "photo 1"},
	} {
		_, err := NewAlbum(context.Background(), c.paths)
		if !errors.Is(err, c.want) || !strings.Contains(fmt.Sprint(err), c.in) {
			t.Errorf("%s: %v, want %v naming %q", c.name, err, c.want, c.in)
		}
	}
}
