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

// The album's timeline, in seconds. The loop's seam is in the middle of the
// hold, where nothing moves: the photos rest with their numbers, leave one
// after the other, the frame is empty for a moment, and they fly in again.
const (
	albumDur   = 9.0
	leaveFrom  = 1.8  // the first photo starts to leave
	leaveTime  = 1.3  // one photo's flight out
	enterTime  = 1.6  // and in
	emptyBeat  = 0.6  // the frame with nothing in it
	badgeDelay = 0.15 // a number comes this long before its photo lands
	badgePop   = 0.45 // and takes this long to grow
)

// Album is the preview of an album: the photos fly into the frame and get
// their numbers, 1 to N in the order they were sent.
type Album struct {
	pics  []*Pic
	rects []image.Rectangle // each photo's place
	from  [][2]float64      // where each flies in from, as an offset from its place
	tilt  []float64         // how much each is turned as it flies, radians
	stag  float64           // between one photo and the next
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
	box := image.Rect(frameX0+frameInset, frameY0+frameInset, frameX1-frameInset, frameY1-frameInset)
	a := &Album{
		rects: layout(aspects, box, gap),
		pics:  make([]*Pic, n),
		from:  make([][2]float64, n),
		tilt:  make([]float64, n),
		stag:  min(0.14, 1.2/float64(n)),
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

	// Each photo comes from outside the frame along the line from its middle
	// to the frame's, turned a little to one side or the other.
	fx, fy := float64(frameX0+frameX1)/2, float64(frameY0+frameY1)/2
	for i, r := range a.rects {
		dx, dy := float64(r.Min.X+r.Max.X)/2-fx, float64(r.Min.Y+r.Max.Y)/2-fy
		if math.Hypot(dx, dy) < 30 { // a photo in the middle: round the clock, by the golden angle
			s, c := math.Sincos(-math.Pi/2 + float64(i)*2.399963)
			dx, dy = c, s
		}
		l := math.Hypot(dx, dy)
		a.from[i] = [2]float64{dx / l * 320, dy / l * 320}
		a.tilt[i] = float64(1-2*(i%2)) * (5 + float64(i*7%4)) * math.Pi / 180
	}
	return a, nil
}

func (a *Album) Size() (int, int)  { return Side, Side }
func (a *Album) Duration() float64 { return albumDur }

// leaves and enters are the moments photo i starts its flight out and in.
func (a *Album) leaves(i int) float64 { return leaveFrom + float64(i)*a.stag }
func (a *Album) enters(i int) float64 {
	return leaveFrom + float64(len(a.pics)-1)*a.stag + leaveTime + emptyBeat + float64(i)*a.stag
}

// state is how far photo i is in its place at t, 0 when it is outside the
// frame and 1 when it rests, and how big its number is, 0 to a little over 1.
func (a *Album) state(i int, t float64) (p, num float64) {
	out, in := a.leaves(i), a.enters(i)
	switch {
	case t < out:
		return 1, 1
	case t < out+leaveTime:
		return 1 - sine(lin(out, out+leaveTime, t)), 1 - smooth(out, out+0.3, t)
	case t < in:
		return 0, 0
	case t < in+enterTime:
		return sine(lin(in, in+enterTime, t)), 0
	}
	return 1, back(lin(in+enterTime-badgeDelay, in+enterTime-badgeDelay+badgePop, t))
}

func (a *Album) Draw(c *Canvas, t float64) {
	n := len(a.pics)
	c.Fill(Field)
	c.header()

	// The viewfinder closes a little when the last photo lands, as a camera
	// does when it has focused.
	land := a.enters(n-1) + enterTime
	focus := math.Pow(math.Sin(math.Pi*lin(land-0.15, land+0.65, t)), 2) * 10
	c.brackets(frameX0+focus, frameY0+focus, frameX1-focus, frameY1-focus, 72, white, 1)

	// The spark while the frame is empty.
	here := 0.0
	ps := make([]float64, n)
	nums := make([]float64, n)
	for i := range n {
		ps[i], nums[i] = a.state(i, t)
		here += ps[i]
	}
	if gone := 1 - smooth(0, 0.5, here/float64(n)); gone > 0 {
		cx, cy := float64(frameX0+frameX1)/2, float64(frameY0+frameY1)/2
		c.spark(cx, cy, 66+7*math.Sin(2*math.Pi*t/albumDur*2), math.Pi/2*t/albumDur, white, gone)
	}

	// Photos in their place first, then the ones in flight above them.
	for pass := range 2 {
		for i := range n {
			if (ps[i] == 1) != (pass == 0) || ps[i] == 0 {
				continue
			}
			r, q := a.rects[i], ps[i]
			pose := Pose{
				DX:    a.from[i][0] * (1 - q),
				DY:    a.from[i][1] * (1 - q),
				Scale: 1 + 0.18*(1-q),
				Rot:   a.tilt[i] * (1 - q),
			}
			c.Photo(a.pics[i], float64(r.Min.X), float64(r.Min.Y), pose, smooth(0, 0.45, q))
			c.number(i+1, r, a.badge, nums[i])
		}
	}
}

// number draws the badge of photo no at the top left of its place r: a white
// square of side size with the figure in the field's colour, grown from the
// corner to scale.
func (c *Canvas) number(no int, r image.Rectangle, size, scale float64) {
	if scale <= 0 {
		return
	}
	b := size * scale
	x, y := float64(r.Min.X), float64(r.Min.Y)
	c.Rect(x, y, x+b, y+b, white, 1)
	c.Figure(Display, math.Round(b*0.86), strconv.Itoa(no), x+b/2, y+b/2, Field, 1)
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
