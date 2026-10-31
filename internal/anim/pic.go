// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // the formats internal/media writes
	_ "image/png"
	"math"
	"os"

	xdraw "golang.org/x/image/draw"
)

// maxSourcePixels is the largest photo the album preview decodes, 16 MP.
// Photos reach it already brought down to 2048 px on the long side by
// internal/media, so this only stops a file that did not come that way.
const maxSourcePixels = 16_000_000

// Pic is a photo cut and scaled to its tile: 8-bit RGB, three bytes a pixel.
type Pic struct {
	W, H int
	Pix  []byte
}

// readConfig reads the size of the JPEG or PNG in f without decoding it, and
// refuses any other format and any picture of more than maxSourcePixels.
func readConfig(f *os.File) (image.Config, error) {
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return cfg, fmt.Errorf("%w: %v", ErrNotAPhoto, err)
	}
	if format != "jpeg" && format != "png" {
		return cfg, fmt.Errorf("%w: %s", ErrNotAPhoto, format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxSourcePixels {
		return cfg, fmt.Errorf("%w: %d×%d", ErrNotAPhoto, cfg.Width, cfg.Height)
	}
	return cfg, nil
}

// photoSize reads the size of the photo at path.
func photoSize(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := readConfig(f)
	return cfg.Width, cfg.Height, err
}

// loadPic decodes the photo at path and scales it to cover w×h, cutting what
// overhangs equally on both sides. A transparent part of a PNG shows deep.
func loadPic(path string, w, h int) (*Pic, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := readConfig(f); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAPhoto, err)
	}
	sb := src.Bounds()
	crop := sb
	if sw, sh := sb.Dx(), sb.Dy(); sw*h > sh*w { // wider than the tile: cut the sides
		cw := int(math.Round(float64(sh) * float64(w) / float64(h)))
		crop = image.Rect(sb.Min.X+(sw-cw)/2, sb.Min.Y, sb.Min.X+(sw-cw)/2+cw, sb.Max.Y)
	} else if sw*h < sh*w { // taller: cut the top and the bottom
		ch := int(math.Round(float64(sw) * float64(h) / float64(w)))
		crop = image.Rect(sb.Min.X, sb.Min.Y+(sh-ch)/2, sb.Max.X, sb.Min.Y+(sh-ch)/2+ch)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	op := xdraw.Src
	if o, ok := src.(interface{ Opaque() bool }); ok && !o.Opaque() {
		xdraw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{level(deep.R), level(deep.G), level(deep.B), 255}), image.Point{}, xdraw.Src)
		op = xdraw.Over
	}
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, op, nil)
	p := &Pic{W: w, H: h, Pix: make([]byte, 3*w*h)}
	for i, j := 0, 0; i < len(dst.Pix); i, j = i+4, j+3 {
		p.Pix[j], p.Pix[j+1], p.Pix[j+2] = dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2]
	}
	return p, nil
}

// Pose is where a photo is: moved by DX, DY from its place, scaled by Scale
// and turned by Rot radians, both about its middle.
type Pose struct{ DX, DY, Scale, Rot float64 }

// Rest is a photo in its place.
var Rest = Pose{Scale: 1}

// Photo paints p with its top left at x, y when it rests, in the pose ps and
// at alpha. A photo at rest on whole pixels is copied as it is, so it stays
// as sharp as the scaling made it; a moving one is sampled bilinearly and its
// edge is anti-aliased.
func (c *Canvas) Photo(p *Pic, x, y float64, ps Pose, alpha float64) {
	if alpha <= 0 || ps.Scale <= 0 {
		return
	}
	a := float32(alpha)
	if ps == Rest && x == math.Trunc(x) && y == math.Trunc(y) {
		x0, y0 := int(x), int(y)
		for py := max(0, y0); py < min(c.H, y0+p.H); py++ {
			for px := max(0, x0); px < min(c.W, x0+p.W); px++ {
				s := p.Pix[3*((py-y0)*p.W+px-x0):][:3:3]
				d := c.Pix[3*(py*c.W+px):][:3:3]
				d[0] += (float32(s[0])/255 - d[0]) * a
				d[1] += (float32(s[1])/255 - d[1]) * a
				d[2] += (float32(s[2])/255 - d[2]) * a
			}
		}
		return
	}
	hw, hh := float64(p.W)/2*ps.Scale, float64(p.H)/2*ps.Scale
	cx, cy := x+float64(p.W)/2+ps.DX, y+float64(p.H)/2+ps.DY
	sn, cs := math.Sincos(ps.Rot)
	ex := math.Abs(cs)*hw + math.Abs(sn)*hh + 1
	ey := math.Abs(sn)*hw + math.Abs(cs)*hh + 1
	x0, x1 := max(0, int(math.Floor(cx-ex))), min(c.W, int(math.Ceil(cx+ex)))
	y0, y1 := max(0, int(math.Floor(cy-ey))), min(c.H, int(math.Ceil(cy+ey)))
	inv := 1 / ps.Scale
	mw, mh := float64(p.W-1), float64(p.H-1)
	for py := y0; py < y1; py++ {
		dy := float64(py) + 0.5 - cy
		for px := x0; px < x1; px++ {
			dx := float64(px) + 0.5 - cx
			// The pixel in the photo's own axes, in destination pixels.
			ux, uy := cs*dx+sn*dy, -sn*dx+cs*dy
			k := cover(max(math.Abs(ux)-hw, math.Abs(uy)-hh)) * a
			if k <= 0 {
				continue
			}
			u := min(max(ux*inv+float64(p.W)/2-0.5, 0), mw)
			v := min(max(uy*inv+float64(p.H)/2-0.5, 0), mh)
			iu, iv := int(u), int(v)
			fu, fv := float32(u-float64(iu)), float32(v-float64(iv))
			ju, jv := min(iu+1, p.W-1), min(iv+1, p.H-1)
			s00 := p.Pix[3*(iv*p.W+iu):][:3:3]
			s10 := p.Pix[3*(iv*p.W+ju):][:3:3]
			s01 := p.Pix[3*(jv*p.W+iu):][:3:3]
			s11 := p.Pix[3*(jv*p.W+ju):][:3:3]
			d := c.Pix[3*(py*c.W+px):][:3:3]
			for i := range 3 {
				top := float32(s00[i]) + (float32(s10[i])-float32(s00[i]))*fu
				bot := float32(s01[i]) + (float32(s11[i])-float32(s01[i]))*fu
				d[i] += ((top+(bot-top)*fv)/255 - d[i]) * k
			}
		}
	}
}
