// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"strconv"
	"sync"
)

// MaxPhotos is how many photos an album preview holds: the most a Telegram
// album has.
const MaxPhotos = 10

var (
	// ErrNoPhotos: nothing to show, or more than MaxPhotos.
	ErrNoPhotos = errors.New("an album preview takes 1 to 10 photos")
	// ErrNotAPhoto: not a JPEG or a PNG of a size internal/media would write.
	ErrNotAPhoto = errors.New("not a photo the preview draws")
)

// Where the photos go: the viewfinder's outer edge, and the box inside it.
const (
	frameX0, frameY0, frameX1, frameY1 = 32, 116, 688, 688
	frameInset                         = stripe + 2*gap
)

// How the album floats: as one piece, the photos never leaving their places,
// it rises and sinks once a loop over its shadow.
const (
	albumDur = 4.0  // seconds: one rise and one sink
	floatUp  = 12.0 // pixels the album rises at the top of its float
	// The collage is kept this far off the frame's top and bottom, for the
	// float and the shadow.
	floatInset = 10
)

// Album is the preview of an album: the photos, numbered 1 to N in the order
// they were sent, float in the viewfinder together.
type Album struct {
	pics  []*Pic
	rects []image.Rectangle // each photo's place
	badge float64           // the side of every number's square
}

// NewAlbum reads the photos at paths and lays them out. A photo keeps its
// proportions, within the limits of the frame.
func NewAlbum(ctx context.Context, paths []string) (*Album, error) {
	n := len(paths)
	if n < 1 || n > MaxPhotos {
		return nil, ErrNoPhotos
	}
	aspects := make([]float64, n)
	for i, p := range paths {
		w, h, err := photoSize(p)
		if err != nil {
			return nil, fmt.Errorf("photo %d: %w", i+1, err)
		}
		// A panorama or a tall strip would leave a sliver of a tile.
		aspects[i] = min(2, max(0.5, float64(w)/float64(h)))
	}
	// The box leaves room above for the float and below for the shadows.
	box := image.Rect(frameX0+frameInset, frameY0+frameInset+floatInset, frameX1-frameInset, frameY1-frameInset-floatInset)
	a := &Album{
		rects: layout(aspects, box, gap),
		pics:  make([]*Pic, n),
	}
	a.badge = badgeSize(a.rects)

	// Decoding is the slow part: a few photos at a time, so a preview of ten
	// does not hold ten decoded photos at once.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	errs := make([]error, n)
	for i := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				errs[i] = ctx.Err()
				return
			}
			a.pics[i], errs[i] = loadPic(paths[i], a.rects[i].Dx(), a.rects[i].Dy())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("photo %d: %w", i+1, err)
		}
	}
	return a, nil
}

func (a *Album) Size() (int, int)  { return Side, Side }
func (a *Album) Duration() float64 { return albumDur }

// height is how high the album is at t: 0 at rest, 1 at the top of its
// float. The time is taken round the loop first, so that the frame at the
// loop's end is the first frame to the last bit. The loop starts at rest:
// that is the frame Telegram shows as its still.
func height(t float64) float64 {
	u := math.Mod(t, albumDur) / albumDur
	return 0.5 - 0.5*math.Cos(2*math.Pi*u)
}

func (a *Album) Draw(c *Canvas, t float64) {
	c.Fill(Field)
	c.header()
	c.brackets(frameX0, frameY0, frameX1, frameY1, 72, white, 1)

	// The shadow stays on the ground: when the album is up it is wider
	// and fainter.
	h := height(t)
	all := a.rects[0]
	for _, r := range a.rects {
		all = all.Union(r)
	}
	c.Shadow(float64(all.Min.X)+2, float64(all.Min.Y)+8, float64(all.Max.X)-2, float64(all.Max.Y)+8, 8+8*h, shade, 0.6-0.25*h)
	// The album moves by whole pixels: a photo between two rows of pixels
	// would be blurred by the sampling, sharper and softer by turns, and
	// cost the encoder far more. A pixel of 720 is too small a step to see.
	y := -math.Round(floatUp * h)
	for i, r := range a.rects {
		c.Photo(a.pics[i], float64(r.Min.X), float64(r.Min.Y)+y, Rest, 1)
		c.number(i+1, float64(r.Min.X), float64(r.Min.Y)+y, a.badge)
	}
}

// number draws the badge of photo no with its top left at x, y: a white
// square of side size with the figure in the field's colour.
func (c *Canvas) number(no int, x, y, size float64) {
	c.Rect(x, y, x+size, y+size, white, 1)
	c.Figure(Display, math.Round(size*0.86), strconv.Itoa(no), x+size/2, y+size/2, Field, 1)
}

// badgeSize is the side of every number's square: a third of the smallest
// photo's short side, so that the numbers are one size and still readable at
// the size a chat shows the picture.
func badgeSize(rects []image.Rectangle) float64 {
	short := math.Inf(1)
	for _, r := range rects {
		short = min(short, float64(min(r.Dx(), r.Dy())))
	}
	return float64(min(72, max(44, int(math.Round(short/3)))) / 2 * 2)
}

// layout places photos of the given aspects, width over height, in rows
// inside box with gap pixels between them. Every way of cutting the photos,
// in order, into at most four rows is tried. The best is the one whose
// smallest photo is the biggest, so that every number can be read and no
// photo is a sliver; the bigger collage breaks a tie. A row's photos share
// one height, so none is cut more than a pixel or two. The collage sits in
// the middle of the box.
func layout(aspects []float64, box image.Rectangle, gap int) []image.Rectangle {
	n := len(aspects)
	bw, bh, g := float64(box.Dx()), float64(box.Dy()), float64(gap)
	const maxRows = 4

	var best []int
	bestSmall, bestArea, bestW := -1.0, -1.0, 0.0
	for mask := 0; mask < 1<<(n-1); mask++ {
		rows := cut(n, mask)
		if len(rows) > maxRows {
			continue
		}
		// With a width wc a row of sum s and k photos is (wc-(k-1)g)/s high,
		// so the collage's height is wc*inv-fixed plus the gaps between rows.
		sums := rowSums(aspects, rows)
		inv, fixed := 0.0, 0.0
		for j, s := range sums {
			inv += 1 / s
			fixed += float64(rows[j]-1) * g / s
		}
		between := float64(len(rows)-1) * g
		wc := bw
		hc := wc*inv - fixed + between
		if hc > bh {
			wc = (bh - between + fixed) / inv
			hc = bh
		}
		if wc <= 0 {
			continue
		}
		small, i := math.Inf(1), 0
		for j, k := range rows {
			h := (wc - float64(k-1)*g) / sums[j]
			for _, a := range aspects[i : i+k] {
				small = min(small, a*h*h)
			}
			i += k
		}
		if small > bestSmall+1e-6 || (small > bestSmall-1e-6 && wc*hc > bestArea+1e-6) {
			best, bestSmall, bestArea, bestW = rows, small, wc*hc, wc
		}
	}

	// The same collage in whole pixels: the width first, then every row's
	// height, then every photo's width, each fixed up on the biggest part so
	// the sums come out exactly.
	wc := int(bestW)
	heights := make([]int, len(best))
	total := (len(best) - 1) * gap
	for j, s := range rowSums(aspects, best) {
		heights[j] = int(math.Round((float64(wc) - float64(best[j]-1)*g) / s))
		total += heights[j]
	}
	want := int(math.Round(bestArea / bestW))
	heights[biggest(heights)] += want - total
	total = want
	x0, y, i := box.Min.X+(box.Dx()-wc)/2, box.Min.Y+(box.Dy()-total)/2, 0
	out := make([]image.Rectangle, 0, n)
	for j, k := range best {
		widths := make([]int, k)
		sum := (k - 1) * gap
		for m := range k {
			widths[m] = int(math.Round(aspects[i+m] * float64(heights[j])))
			sum += widths[m]
		}
		widths[biggest(widths)] += wc - sum
		x := x0
		for m := range k {
			out = append(out, image.Rect(x, y, x+widths[m], y+heights[j]))
			x += widths[m] + gap
		}
		y += heights[j] + gap
		i += k
	}
	return out
}

// rowSums adds up the aspects of every row.
func rowSums(aspects []float64, rows []int) []float64 {
	sums := make([]float64, len(rows))
	i := 0
	for j, k := range rows {
		for _, a := range aspects[i : i+k] {
			sums[j] += a
		}
		i += k
	}
	return sums
}

// cut turns the bits of mask into row lengths: a set bit i ends a row after
// photo i.
func cut(n, mask int) []int {
	var rows []int
	run := 1
	for i := range n - 1 {
		if mask>>i&1 == 1 {
			rows = append(rows, run)
			run = 0
		}
		run++
	}
	return append(rows, run)
}

func biggest(v []int) int {
	best := 0
	for i, x := range v {
		if x > v[best] {
			best = i
		}
	}
	return best
}
