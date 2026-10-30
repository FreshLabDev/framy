// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A video goes through the same kind of subprocess as a photo
// (docs/design.md, Storage and retention). H.264 in yuv420p with AAC or no
// sound, upright, at 1080p or less and at a bitrate a model takes, is only
// copied into an MP4 with faststart. Anything else, and every cut, is
// encoded again to H.264 High yuv420p with AAC and even sizes. The metadata
// goes either way, and with it the GPS position of a phone's clip.
const (
	// DefaultMaxSeconds is the longest clip framy keeps. No model takes more
	// than 30 s; a longer clip is cut to the part the person picks.
	DefaultMaxSeconds = 60
	// DefaultVideoLongSide is the long side a clip is brought down to when it
	// is encoded again: 1080p, which no editor exceeds on its input.
	DefaultVideoLongSide = 1920
	// DefaultVideoTimeout bounds each subprocess of a clip.
	DefaultVideoTimeout = 4 * time.Minute
	// MinSeconds is the shortest part that can be cut.
	MinSeconds = 0.1
	// maxFPS is the highest frame rate kept.
	maxFPS = 60
	// copyBitrate is the highest bitrate copied as it is, in bits a second,
	// and maxBitrate the cap of an encoded clip: at 8 Mbit/s 48 s of video
	// stay under the 48 MiB the strictest editor takes.
	copyBitrate = 10_000_000
	maxBitrate  = "8M"
)

var (
	// ErrUnsupportedVideo: not an MP4, MOV, WebM or Matroska file with a
	// video in it.
	ErrUnsupportedVideo = errors.New("not a video framy takes")
	// ErrUnreadableVideo: the container is right but the clip cannot be read.
	ErrUnreadableVideo = errors.New("the video cannot be read")
	// ErrVideoTooLarge: a frame has more pixels than MaxPixels.
	ErrVideoTooLarge = errors.New("the video's frames are too large")
	// ErrBadCut: the part asked for is not in the clip.
	ErrBadCut = errors.New("that part is not in the clip")
)

// TooLongError is a clip, or the part of it asked for, over MaxSeconds.
type TooLongError struct {
	Seconds, Max float64
}

func (e *TooLongError) Error() string {
	return fmt.Sprintf("the clip is %.1f s and framy keeps at most %g s: pick a part of it", e.Seconds, e.Max)
}

// Cut is the part of a clip to keep: Seconds from Start.
type Cut struct {
	Start, Seconds float64
}

// Valid: both are numbers, Start is not negative and Seconds is at least
// MinSeconds.
func (c Cut) Valid() bool {
	for _, f := range []float64{c.Start, c.Seconds} {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
	}
	return c.Start >= 0 && c.Seconds >= MinSeconds
}

// Video is a clip as framy keeps it.
type Video struct {
	Path        string
	ContentType string
	// Width and Height are as the clip is shown, after its rotation.
	Width, Height int
	Seconds       float64
	FPS           float64
	Audio         bool
	Bytes         int64
	// Copied: the streams were copied, not encoded again.
	Copied bool
	// HDR: the source was HDR and was brought to SDR, by tone mapping where
	// this ffmpeg has zscale and by its colour matrix alone where it has not.
	HDR bool
}

func (c *Converter) maxSeconds() float64 {
	if c.MaxSeconds > 0 {
		return c.MaxSeconds
	}
	return DefaultMaxSeconds
}

// MaxClipSeconds is the longest clip this converter keeps.
func (c *Converter) MaxClipSeconds() float64 { return c.maxSeconds() }

func (c *Converter) videoLongSide() int {
	if c.VideoLongSide > 0 {
		return c.VideoLongSide
	}
	return DefaultVideoLongSide
}

func (c *Converter) videoTimeout() time.Duration {
	if c.VideoTimeout > 0 {
		return c.VideoTimeout
	}
	return DefaultVideoTimeout
}

// The demuxers framy reads a clip with. ffmpeg never guesses one from the
// bytes: a file framy calls an MP4 is read as nothing else.
const (
	demuxMOV      = "mov"
	demuxMatroska = "matroska"
)

// SniffVideo names the demuxer of a clip from its first bytes: "mov" for an
// MP4, a MOV or a 3GP, "matroska" for WebM and MKV, or "" for anything
// else, a HEIC or an AVIF included.
func SniffVideo(head []byte) string {
	switch {
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		if isobmff(head) != "" {
			return ""
		}
		return demuxMOV
	case len(head) >= 4 && head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		return demuxMatroska
	}
	return ""
}

// CheckVideo tells whether this ffmpeg can make what NormalizeVideo makes:
// libx264 and the native AAC encoder.
func (c *Converter) CheckVideo(ctx context.Context) error {
	out, err := c.run(ctx, c.ffmpeg(), []string{"-hide_banner", "-encoders"})
	if err != nil {
		return fmt.Errorf("%s -encoders: %w", c.ffmpeg(), err)
	}
	for _, need := range []string{" libx264 ", " aac "} {
		if !strings.Contains(string(out), need) {
			return fmt.Errorf("%s lacks the %s encoder", c.ffmpeg(), strings.TrimSpace(need))
		}
	}
	return nil
}

// toneMaps tells whether this ffmpeg has zscale, which tone mapping needs.
func (c *Converter) toneMaps(ctx context.Context) bool {
	c.zscaleOnce.Do(func() {
		out, err := c.run(ctx, c.ffmpeg(), []string{"-hide_banner", "-filters"})
		c.zscale = err == nil && strings.Contains(string(out), " zscale ")
	})
	return c.zscale
}

// clip is what ffprobe says of a video.
type clip struct {
	demuxer       string
	brand         string
	codec, pixFmt string
	transfer      string
	width, height int
	rotation      int
	fps, seconds  float64
	bitrate       float64
	videoIndex    int
	audioIndex    int
	audioCodec    string
	audioChannels int
}

func (k clip) audio() bool { return k.audioIndex >= 0 }

// shown is the clip's size as a player shows it.
func (k clip) shown() (int, int) {
	if k.rotation%180 != 0 {
		return k.height, k.width
	}
	return k.width, k.height
}

func (k clip) hdr() bool {
	return k.transfer == "smpte2084" || k.transfer == "arib-std-b67"
}

// contentType names the container as a browser does.
func (k clip) contentType() string {
	switch {
	case k.demuxer == demuxMatroska && k.brand == "webm":
		return "video/webm"
	case k.demuxer == demuxMatroska:
		return "video/x-matroska"
	case k.brand == "qt  ":
		return "video/quicktime"
	}
	return "video/mp4"
}

type videoProbeOut struct {
	Streams []struct {
		Index         int    `json:"index"`
		CodecType     string `json:"codec_type"`
		CodecName     string `json:"codec_name"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		PixFmt        string `json:"pix_fmt"`
		ColorTransfer string `json:"color_transfer"`
		AvgFrameRate  string `json:"avg_frame_rate"`
		RFrameRate    string `json:"r_frame_rate"`
		Duration      string `json:"duration"`
		Channels      int    `json:"channels"`
		Disposition   struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		Tags struct {
			Rotate string `json:"rotate"`
		} `json:"tags"`
		SideData []struct {
			Type     string  `json:"side_data_type"`
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

// ProbeVideo reads what the clip at path is: its first video stream that is
// not a cover picture, its first sound, its length and its rotation.
func (c *Converter) ProbeVideo(ctx context.Context, path string) (Video, error) {
	head, err := readHead(path)
	if err != nil {
		return Video{}, err
	}
	k, err := c.probeVideo(ctx, head, path)
	if err != nil {
		return Video{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return Video{}, err
	}
	w, h := k.shown()
	return Video{
		Path: path, ContentType: k.contentType(), Width: w, Height: h,
		Seconds: k.seconds, FPS: k.fps, Audio: k.audio(), Bytes: st.Size(), HDR: k.hdr(),
	}, nil
}

func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	if n == 0 {
		return nil, fmt.Errorf("%w: the file is empty", ErrUnsupportedVideo)
	}
	return head[:n], nil
}

// webmDocType is the EBML DocType element of a WebM file, which names it
// within the first bytes.
var webmDocType = []byte{0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}

func (c *Converter) probeVideo(ctx context.Context, head []byte, in string) (clip, error) {
	demuxer := SniffVideo(head)
	if demuxer == "" {
		return clip{}, ErrUnsupportedVideo
	}
	raw, err := c.run(ctx, c.ffprobe(), []string{
		"-hide_banner", "-v", "error",
		"-protocol_whitelist", "file",
		"-f", demuxer,
		"-show_streams", "-show_format", "-of", "json", "file:" + in,
	})
	if err != nil {
		if ctx.Err() != nil {
			return clip{}, err
		}
		return clip{}, fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
	}
	var out videoProbeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return clip{}, fmt.Errorf("%w: ffprobe: %v", ErrUnreadableVideo, err)
	}
	k := clip{demuxer: demuxer, videoIndex: -1, audioIndex: -1}
	switch {
	case demuxer == demuxMOV:
		k.brand = string(head[8:12])
	case strings.Contains(string(head), string(webmDocType)):
		k.brand = "webm"
	}
	for _, s := range out.Streams {
		switch {
		case s.CodecType == "video" && s.Disposition.AttachedPic == 0 && s.Width > 0 && s.Height > 0 && k.videoIndex < 0:
			k.videoIndex, k.codec, k.pixFmt, k.transfer = s.Index, s.CodecName, s.PixFmt, s.ColorTransfer
			k.width, k.height = s.Width, s.Height
			k.fps = rate(s.AvgFrameRate)
			if k.fps == 0 {
				k.fps = rate(s.RFrameRate)
			}
			k.seconds = number(s.Duration)
			for _, d := range s.SideData {
				if d.Type == "Display Matrix" {
					k.rotation = int(math.Round(d.Rotation))
				}
			}
			if k.rotation == 0 && s.Tags.Rotate != "" {
				k.rotation, _ = strconv.Atoi(s.Tags.Rotate)
			}
			k.rotation = ((k.rotation % 360) + 360) % 360
		case s.CodecType == "audio" && k.audioIndex < 0:
			k.audioIndex, k.audioCodec, k.audioChannels = s.Index, s.CodecName, s.Channels
		}
	}
	if k.videoIndex < 0 {
		return clip{}, fmt.Errorf("%w: no video in the file", ErrUnsupportedVideo)
	}
	if d := number(out.Format.Duration); d > 0 {
		k.seconds = d
	}
	if k.seconds <= 0 {
		return clip{}, fmt.Errorf("%w: the clip has no length", ErrUnreadableVideo)
	}
	if int64(k.width)*int64(k.height) > c.maxPixels() {
		return clip{}, fmt.Errorf("%w: %d×%d", ErrVideoTooLarge, k.width, k.height)
	}
	k.bitrate = number(out.Format.BitRate)
	if k.bitrate == 0 {
		if st, err := os.Stat(in); err == nil {
			k.bitrate = float64(st.Size()) * 8 / k.seconds
		}
	}
	return k, nil
}

// rate reads ffprobe's "30000/1001".
func rate(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return number(s)
	}
	n, d := number(a), number(b)
	if d == 0 {
		return 0
	}
	return n / d
}

func number(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return f
}

// NormalizeVideo makes the clip at dir/In, or the part cut of it, into what
// framy keeps, written next to it. A clip over MaxSeconds, whole or as cut,
// comes back as a *TooLongError; a part that is not in the clip as
// ErrBadCut; a file that is not a clip framy takes as ErrUnsupportedVideo,
// ErrUnreadableVideo or ErrVideoTooLarge.
func (c *Converter) NormalizeVideo(ctx context.Context, dir string, cut *Cut) (Video, error) {
	in := filepath.Join(dir, In)
	head, err := readHead(in)
	if err != nil {
		return Video{}, err
	}
	k, err := c.probeVideo(ctx, head, in)
	if err != nil {
		return Video{}, err
	}
	start, length := 0.0, k.seconds
	if cut != nil {
		if !cut.Valid() || cut.Start >= k.seconds {
			return Video{}, ErrBadCut
		}
		start, length = cut.Start, math.Min(cut.Seconds, k.seconds-cut.Start)
		if length < MinSeconds {
			return Video{}, ErrBadCut
		}
	}
	// A clip a frame over the limit is not refused for the frame.
	if length > c.maxSeconds()+0.05 {
		return Video{}, &TooLongError{Seconds: length, Max: c.maxSeconds()}
	}
	out := filepath.Join(dir, "out.mp4")
	v := Video{ContentType: "video/mp4", HDR: k.hdr()}
	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-protocol_whitelist", "file"}
	video, audio := "0:"+strconv.Itoa(k.videoIndex), "0:"+strconv.Itoa(k.audioIndex)
	if cut == nil && c.copyable(k) {
		v.Copied = true
		args = append(args, "-f", k.demuxer, "-i", "file:"+in, "-map", video)
		if k.audio() {
			args = append(args, "-map", audio)
		}
		args = append(args, "-c", "copy")
	} else {
		if cut != nil {
			// Before the input: a seek that is exact, since every frame
			// is decoded again.
			args = append(args, "-ss", seconds(start), "-t", seconds(length))
		}
		args = append(args, "-max_pixels", strconv.FormatInt(c.maxPixels(), 10),
			"-f", k.demuxer, "-i", "file:"+in, "-map", video,
			"-vf", c.videoFilters(ctx, k, "yuv420p"),
			"-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p",
			"-preset", "veryfast", "-crf", "20", "-maxrate", maxBitrate, "-bufsize", "16M")
		if k.hdr() {
			args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
		}
		if k.audio() {
			args = append(args, "-map", audio, "-c:a", "aac", "-b:a", "160k")
			if k.audioChannels > 2 {
				args = append(args, "-ac", "2")
			}
		}
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-movflags", "+faststart", "-f", "mp4", "-y", "file:"+out)
	if _, err := c.runFor(ctx, c.videoTimeout(), c.ffmpeg(), args); err != nil {
		if ctx.Err() != nil {
			return Video{}, err
		}
		return Video{}, fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
	}
	outHead, err := readHead(out)
	if err != nil {
		return Video{}, fmt.Errorf("%w: ffmpeg wrote no clip", ErrUnreadableVideo)
	}
	got, err := c.probeVideo(ctx, outHead, out)
	if err != nil {
		return Video{}, fmt.Errorf("the clip framy wrote: %w", err)
	}
	st, err := os.Stat(out)
	if err != nil {
		return Video{}, err
	}
	v.Path, v.Bytes = out, st.Size()
	v.Width, v.Height = got.shown()
	v.Seconds, v.FPS, v.Audio = got.seconds, got.fps, got.audio()
	return v, nil
}

// copyable: the streams can be copied as they are. A rotated clip is
// encoded again so its frames are upright for every model, whatever a model
// makes of a display matrix.
func (c *Converter) copyable(k clip) bool {
	w, h := k.shown()
	return k.demuxer == demuxMOV && k.codec == "h264" && k.pixFmt == "yuv420p" && !k.hdr() &&
		k.rotation == 0 && max(w, h) <= c.videoLongSide() && k.fps <= maxFPS+0.01 &&
		k.bitrate > 0 && k.bitrate <= copyBitrate &&
		(!k.audio() || k.audioCodec == "aac")
}

// videoFilters bring the long side to VideoLongSide at most and the sizes to
// even numbers, an HDR clip to SDR and the frame rate to 60 at most, and end
// in pixFmt. The rotation comes before them, by ffmpeg's autorotate.
func (c *Converter) videoFilters(ctx context.Context, k clip, pixFmt string) string {
	n := strconv.Itoa(c.videoLongSide())
	f := []string{
		"scale=w='min(" + n + ",iw)':h='min(" + n + ",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2:flags=bicubic",
		"setsar=1",
	}
	if k.hdr() {
		f = append(f, c.toSDR(ctx, k)...)
	}
	if k.fps > maxFPS+0.01 {
		f = append(f, "fps="+strconv.Itoa(maxFPS))
	}
	return strings.Join(append(f, "format="+pixFmt), ",")
}

// toSDR maps an HDR clip, PQ or HLG in BT.2020, to BT.709: by zscale and
// Hable's curve where this ffmpeg has zscale, and by the colour matrix
// alone, a little flat, where it has not.
func (c *Converter) toSDR(ctx context.Context, k clip) []string {
	// The frames say what they are now, or the encoder writes the source's
	// HDR tags over SDR pictures.
	tags := "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709"
	if !c.toneMaps(ctx) {
		return []string{"scale=in_color_matrix=bt2020:out_color_matrix=bt709", tags}
	}
	return []string{
		"zscale=tin=" + k.transfer + ":min=2020_ncl:pin=2020:t=linear:npl=100",
		"format=gbrpf32le",
		"zscale=p=709",
		"tonemap=tonemap=hable:desat=0",
		"zscale=t=709:m=709:r=tv",
		tags,
	}
}

func seconds(s float64) string { return strconv.FormatFloat(s, 'f', 3, 64) }

// fadeBack is how far before the end the frame is taken when the last one
// is flat, as it is when a clip fades out: within the design's 0.2 to 0.5 s.
const fadeBack = 0.35

// LastFrame writes the last frame of the clip at dir/In as a PNG next to
// it, for "Continue" on a model that starts from a frame: or the frame
// 0.35 s before the end when the last one is a single colour.
func (c *Converter) LastFrame(ctx context.Context, dir string) (string, error) {
	in := filepath.Join(dir, In)
	head, err := readHead(in)
	if err != nil {
		return "", err
	}
	k, err := c.probeVideo(ctx, head, in)
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "frame.png")
	frame := func(seek, limit []string) error {
		args := append([]string{"-hide_banner", "-nostdin", "-v", "error", "-protocol_whitelist", "file"}, seek...)
		args = append(args, "-max_pixels", strconv.FormatInt(c.maxPixels(), 10),
			"-f", k.demuxer, "-i", "file:"+in, "-map", "0:"+strconv.Itoa(k.videoIndex),
			"-vf", c.videoFilters(ctx, k, "rgb24"), "-map_metadata", "-1")
		args = append(args, limit...)
		args = append(args, "-c:v", "png", "-f", "image2", "-update", "1", "-y", "file:"+out)
		if _, err := c.runFor(ctx, c.videoTimeout(), c.ffmpeg(), args); err != nil {
			if ctx.Err() != nil {
				return err
			}
			return fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
		}
		if st, err := os.Stat(out); err != nil || st.Size() == 0 {
			return fmt.Errorf("%w: ffmpeg wrote no frame", ErrUnreadableVideo)
		}
		return nil
	}
	// The last second, each frame written over the one before.
	if err := frame([]string{"-sseof", "-1"}, nil); err != nil {
		return "", err
	}
	if flat, err := flatPNG(out); err != nil || !flat || k.seconds <= fadeBack*2 {
		return out, err
	}
	if err := frame([]string{"-ss", seconds(k.seconds - fadeBack)}, []string{"-frames:v", "1"}); err != nil {
		return "", err
	}
	return out, nil
}

// flatPNG tells whether a picture is one colour, near enough: the frame a
// fade ends on.
func flatPNG(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
	}
	return flat(img), nil
}

// flat: the brightness of a 32×32 grid of points varies by less than 3 of
// 255.
func flat(img image.Image) bool {
	b := img.Bounds()
	if b.Empty() {
		return true
	}
	const grid = 32
	var sum, sq float64
	for y := range grid {
		for x := range grid {
			r, g, bl, _ := img.At(b.Min.X+x*b.Dx()/grid, b.Min.Y+y*b.Dy()/grid).RGBA()
			l := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 257
			sum += l
			sq += l * l
		}
	}
	n := float64(grid * grid)
	mean := sum / n
	return math.Sqrt(math.Max(0, sq/n-mean*mean)) < 3
}
