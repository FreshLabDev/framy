// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ThumbLongSide is the long side of a thumbnail at most: the site's library
// shows a grid of them.
const ThumbLongSide = 480

// CheckThumb tells whether this ffmpeg can write a thumbnail: it needs the
// libwebp encoder, which the ffmpeg of framy's image has.
func (c *Converter) CheckThumb(ctx context.Context) error {
	out, err := c.run(ctx, c.ffmpeg(), []string{"-hide_banner", "-encoders"})
	if err != nil {
		return fmt.Errorf("%s -encoders: %w", c.ffmpeg(), err)
	}
	if !strings.Contains(string(out), " libwebp ") {
		return fmt.Errorf("%s lacks the libwebp encoder", c.ffmpeg())
	}
	return nil
}

// Thumb writes a WebP of the picture at in to out and returns its size. It
// is at most ThumbLongSide on the long side and never larger than the
// picture, upright, in sRGB as Normalize makes it, and keeps the alpha. in
// is a file framy keeps, read as Normalize reads one: by the format its
// bytes name, and refused with ErrUnsupported, ErrTooLarge or ErrUnreadable.
func (c *Converter) Thumb(ctx context.Context, in, out string) (width, height int, err error) {
	f, err := os.Open(in)
	if err != nil {
		return 0, 0, err
	}
	h, err := readFile(f)
	f.Close()
	switch {
	case errors.Is(err, errNoMeta):
		return 0, 0, fmt.Errorf("%w: %v", ErrUnreadable, err)
	case err != nil:
		return 0, 0, err
	case h.format == "":
		return 0, 0, ErrUnsupported
	case h.pixels() > c.maxPixels():
		return 0, 0, fmt.Errorf("%w: %d×%d", ErrTooLarge, h.width, h.height)
	}
	p, err := c.probe(ctx, h.format, in)
	if err != nil {
		return 0, 0, err
	}
	pixFmt := "yuv420p"
	if p.alpha {
		pixFmt = "yuva420p"
	}
	args := []string{
		"-hide_banner", "-nostdin", "-v", "error",
		"-protocol_whitelist", "file",
		"-max_pixels", strconv.FormatInt(c.maxPixels(), 10),
		"-f", demuxer[h.format], "-i", "file:" + in,
		"-filter_complex", p.input + fit(ThumbLongSide, h.p3) + ",format=" + pixFmt + "[v]", "-map", "[v]",
		"-map_metadata", "-1", "-frames:v", "1",
		"-c:v", "libwebp", "-quality", "80", "-compression_level", "4",
		"-f", "webp", "-y", "file:" + out,
	}
	if _, err := c.run(ctx, c.ffmpeg(), args); err != nil {
		if ctx.Err() != nil {
			return 0, 0, err
		}
		return 0, 0, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	g, err := os.Open(out)
	if err != nil {
		return 0, 0, err
	}
	defer g.Close()
	oh, err := readFile(g)
	if err != nil {
		return 0, 0, err
	}
	if oh.format != WebP || oh.width == 0 || oh.height == 0 {
		return 0, 0, fmt.Errorf("%w: ffmpeg wrote no WebP", ErrUnreadable)
	}
	return oh.width, oh.height, nil
}
