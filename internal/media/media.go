// SPDX-License-Identifier: Apache-2.0

// Package media turns the photos people bring into what models and Telegram
// read well (docs/design.md, Storage and retention): one ffmpeg subprocess
// per file, under a name framy picks, reading local files only. It rotates
// by EXIF, drops every piece of metadata with the GPS, turns Display P3 into
// sRGB and brings the long side down to 2048 px. The output is JPEG, or PNG
// when the picture has alpha.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxPixels is the largest picture framy takes, about 64 MP: an
	// iPhone's 48 MP fits, a decompression bomb does not.
	DefaultMaxPixels = 64_000_000
	// DefaultLongSide is the long side of a normalized picture.
	DefaultLongSide = 2048
	DefaultTimeout  = time.Minute
)

var (
	// ErrUnsupported: not a JPEG, PNG, WebP, HEIC or AVIF.
	ErrUnsupported = errors.New("not an image framy takes")
	// ErrTooLarge: more pixels than MaxPixels.
	ErrTooLarge = errors.New("the picture is too large")
	// ErrUnreadable: the format is right but the picture cannot be decoded.
	ErrUnreadable = errors.New("the picture cannot be read")
)

// Converter runs ffprobe and ffmpeg. The zero value finds both on PATH.
type Converter struct {
	FFmpeg, FFprobe string
	MaxPixels       int64
	LongSide        int
	// Timeout bounds each subprocess of a picture.
	Timeout time.Duration

	// MaxSeconds is the longest clip kept, LongSide's counterpart for video
	// is VideoLongSide, and VideoTimeout bounds each subprocess of a clip.
	MaxSeconds    float64
	VideoLongSide int
	VideoTimeout  time.Duration

	zscaleOnce sync.Once
	zscale     bool
}

// Image is a normalized picture.
type Image struct {
	Path        string
	ContentType string
	Width       int
	Height      int
	Bytes       int64
	// Source is the format the picture came in.
	Source Format
}

func (c *Converter) ffmpeg() string  { return or(c.FFmpeg, "ffmpeg") }
func (c *Converter) ffprobe() string { return or(c.FFprobe, "ffprobe") }

func (c *Converter) maxPixels() int64 {
	if c.MaxPixels > 0 {
		return c.MaxPixels
	}
	return DefaultMaxPixels
}

func (c *Converter) longSide() int {
	if c.LongSide > 0 {
		return c.LongSide
	}
	return DefaultLongSide
}

func (c *Converter) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Check tells whether this ffmpeg can do what Normalize asks: the colour
// options of scale (FFmpeg 8.0), the sidedata filter, and the decoders of
// every format framy takes.
func (c *Converter) Check(ctx context.Context) error {
	for _, need := range []struct {
		args []string
		want [][]string
	}{
		{[]string{"-h", "filter=scale"}, [][]string{{"in_primaries"}, {"out_transfer"}}},
		{[]string{"-h", "filter=sidedata"}, [][]string{{"ICC_PROFILE"}}},
		{[]string{"-decoders"}, [][]string{{" hevc "}, {" mjpeg "}, {" png "}, {" webp "}, {" libdav1d ", " av1 ", " libaom-av1 "}}},
	} {
		out, err := c.run(ctx, c.ffmpeg(), append([]string{"-hide_banner"}, need.args...))
		if err != nil {
			return fmt.Errorf("%s %s: %w", c.ffmpeg(), strings.Join(need.args, " "), err)
		}
		for _, alts := range need.want {
			found := false
			for _, w := range alts {
				found = found || strings.Contains(string(out), w)
			}
			if !found {
				return fmt.Errorf("%s lacks %s: FFmpeg 8.0 or newer with the HEVC and AV1 decoders is needed", c.ffmpeg(), strings.TrimSpace(alts[0]))
			}
		}
	}
	return nil
}

// In is the file Normalize reads in its directory. ffmpeg reads it under
// this name, so nothing in a person's file name can become an option or a
// protocol.
const In = "in"

// Normalize converts the picture at dir/In, in a directory of the caller's
// that holds nothing else of anyone's, and writes the result next to it. It
// returns ErrUnsupported, ErrTooLarge or ErrUnreadable for a file that is
// not a picture framy takes.
func (c *Converter) Normalize(ctx context.Context, dir string) (Image, error) {
	in := filepath.Join(dir, In)
	f, err := os.Open(in)
	if err != nil {
		return Image{}, err
	}
	h, err := readFile(f)
	f.Close()
	switch {
	case errors.Is(err, errNoMeta):
		return Image{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	case err != nil:
		return Image{}, err
	case h.format == "":
		return Image{}, ErrUnsupported
	case h.pixels() > c.maxPixels():
		return Image{}, fmt.Errorf("%w: %d×%d", ErrTooLarge, h.width, h.height)
	}
	p, err := c.probe(ctx, h.format, in)
	if err != nil {
		return Image{}, err
	}
	if int64(p.width)*int64(p.height) > c.maxPixels() {
		return Image{}, fmt.Errorf("%w: %d×%d", ErrTooLarge, p.width, p.height)
	}
	out, ct, codec := filepath.Join(dir, "out.jpg"), "image/jpeg", []string{"-c:v", "mjpeg", "-q:v", "2"}
	if p.alpha {
		out, ct, codec = filepath.Join(dir, "out.png"), "image/png", []string{"-c:v", "png"}
	}
	args := []string{
		"-hide_banner", "-nostdin", "-v", "error",
		"-protocol_whitelist", "file",
		"-max_pixels", strconv.FormatInt(c.maxPixels(), 10),
		"-f", demuxer[h.format], "-i", "file:" + in,
		"-filter_complex", p.input + c.filters(h.p3) + "[v]", "-map", "[v]",
		"-map_metadata", "-1", "-frames:v", "1",
	}
	args = append(args, codec...)
	args = append(args, "-f", "image2", "-update", "1", "-y", "file:"+out)
	if _, err := c.run(ctx, c.ffmpeg(), args); err != nil {
		if ctx.Err() != nil {
			return Image{}, err
		}
		return Image{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	st, err := os.Stat(out)
	if err != nil {
		return Image{}, err
	}
	img := Image{Path: out, ContentType: ct, Bytes: st.Size(), Source: h.format}
	g, err := os.Open(out)
	if err != nil {
		return Image{}, err
	}
	defer g.Close()
	oh, err := readFile(g)
	if err != nil {
		return Image{}, err
	}
	img.Width, img.Height = oh.width, oh.height
	if img.Width == 0 || img.Height == 0 {
		return Image{}, fmt.Errorf("%w: ffmpeg wrote no picture", ErrUnreadable)
	}
	return img, nil
}

// filters fit the picture into the long side and, for Display P3, convert
// the colours to sRGB and drop the profile, so nothing converts them twice.
// The rotation from EXIF comes before them, by ffmpeg's autorotate.
func (c *Converter) filters(p3 bool) string {
	n := strconv.Itoa(c.longSide())
	f := "scale=w='min(" + n + ",iw)':h='min(" + n + ",ih)':force_original_aspect_ratio=decrease:flags=lanczos"
	if p3 {
		f += ":in_primaries=smpte432:in_transfer=iec61966-2-1:out_primaries=bt709:out_transfer=iec61966-2-1" +
			",sidedata=mode=delete:type=ICC_PROFILE"
	}
	return f
}

// probed is what ffprobe says of a file.
type probed struct {
	// input is the filter graph's input: the tile grid of a HEIC, or the
	// primary picture.
	input         string
	width, height int
	alpha         bool
}

type probeOut struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecType   string `json:"codec_type"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		PixFmt      string `json:"pix_fmt"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
	} `json:"streams"`
	StreamGroups []struct {
		Index       int    `json:"index"`
		Type        string `json:"type"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
		Components []struct {
			Width         int `json:"width"`
			Height        int `json:"height"`
			Subcomponents []struct {
				StreamIndex int `json:"stream_index"`
			} `json:"subcomponents"`
		} `json:"components"`
	} `json:"stream_groups"`
}

func (c *Converter) probe(ctx context.Context, f Format, in string) (probed, error) {
	raw, err := c.run(ctx, c.ffprobe(), []string{
		"-hide_banner", "-v", "error",
		"-protocol_whitelist", "file",
		"-max_pixels", strconv.FormatInt(c.maxPixels(), 10),
		"-f", demuxer[f],
		"-show_streams", "-show_stream_groups", "-of", "json", "file:" + in,
	})
	if err != nil {
		if ctx.Err() != nil {
			return probed{}, err
		}
		return probed{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	var out probeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return probed{}, fmt.Errorf("%w: ffprobe: %v", ErrUnreadable, err)
	}
	pixFmt := map[int]string{}
	for _, s := range out.Streams {
		pixFmt[s.Index] = s.PixFmt
	}
	// A HEIC's tiles are joined by the grid the file names first, or the
	// one it marks as the default.
	grid := -1
	for i, g := range out.StreamGroups {
		if g.Type != "Tile Grid" || len(g.Components) == 0 {
			continue
		}
		if grid < 0 || g.Disposition.Default == 1 {
			grid = i
		}
	}
	if grid >= 0 {
		g := out.StreamGroups[grid]
		p := probed{input: "[0:g:" + strconv.Itoa(g.Index) + "]", width: g.Components[0].Width, height: g.Components[0].Height}
		if sub := g.Components[0].Subcomponents; len(sub) > 0 {
			p.alpha = hasAlpha(pixFmt[sub[0].StreamIndex])
		}
		return p, nil
	}
	primary := -1
	for i, s := range out.Streams {
		if s.CodecType != "video" || s.Width == 0 || s.Height == 0 {
			continue
		}
		if primary < 0 || (s.Disposition.Default == 1 && out.Streams[primary].Disposition.Default != 1) {
			primary = i
		}
	}
	if primary < 0 {
		return probed{}, fmt.Errorf("%w: no picture in the file", ErrUnreadable)
	}
	s := out.Streams[primary]
	return probed{input: "[0:" + strconv.Itoa(s.Index) + "]", width: s.Width, height: s.Height, alpha: hasAlpha(s.PixFmt)}, nil
}

// hasAlpha reads an ffmpeg pixel format: rgba, ya8, yuva420p, gbrap and
// the rest, or a palette, which may hold transparent entries.
func hasAlpha(pixFmt string) bool {
	switch {
	case pixFmt == "pal8":
		return true
	case strings.HasPrefix(pixFmt, "ya"), strings.HasPrefix(pixFmt, "yuva"), strings.HasPrefix(pixFmt, "gbrap"):
		return true
	}
	for _, p := range []string{"rgba", "bgra", "argb", "abgr"} {
		if strings.HasPrefix(pixFmt, p) {
			return true
		}
	}
	return false
}

// stderrTail is how much of ffmpeg's complaint goes into an error.
const stderrTail = 1024

// run starts a subprocess with an empty environment and returns its output.
func (c *Converter) run(ctx context.Context, name string, args []string) ([]byte, error) {
	return c.runFor(ctx, c.timeout(), name, args)
}

// runFor is run with its own timeout, for the longer work of a clip.
func (c *Converter) runFor(ctx context.Context, timeout time.Duration, name string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{}
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > stderrTail {
			msg = "…" + msg[len(msg)-stderrTail:]
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(name), ctx.Err())
		}
		if msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.Bytes(), nil
}
