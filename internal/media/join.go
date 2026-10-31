// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The files Join reads in its directory: the clip a run went on from, and
// the video the run made from its last frame.
const (
	JoinFirst = "first"
	JoinNext  = "next"
)

// MaxJoinedSeconds bounds a joined clip. Each Continue makes the clip
// longer, and it is encoded again whole each time.
const MaxJoinedSeconds = 180

// joinFPS is the frame rate of a join whose clips say none.
const joinFPS = 24

// ErrNothingToJoin: the next part has no frame after its first.
var ErrNothingToJoin = errors.New("the next part is a single frame")

// Join writes the clip at dir/JoinFirst, up to the frame at upTo seconds
// (0 for the whole clip), followed by dir/JoinNext without its first
// frame, which is the frame it started from: one MP4 next to them. Both
// take the larger of the two sizes, the other padded where the shapes
// differ, and the higher frame rate. A part without sound gets silence
// when the other has some. A join over MaxJoinedSeconds comes back as a
// *TooLongError.
func (c *Converter) Join(ctx context.Context, dir string, upTo float64) (Video, error) {
	first, next := filepath.Join(dir, JoinFirst), filepath.Join(dir, JoinNext)
	a, err := c.readClip(ctx, first)
	if err != nil {
		return Video{}, fmt.Errorf("the clip: %w", err)
	}
	b, err := c.readClip(ctx, next)
	if err != nil {
		return Video{}, fmt.Errorf("the next part: %w", err)
	}
	lenA := a.seconds
	if upTo > 0 {
		// Through the frame at upTo: it is the one the next part starts from.
		lenA = math.Min(a.seconds, upTo+1/frameRate(a))
	}
	skip := 1 / frameRate(b)
	lenB := b.seconds - skip
	if lenB < MinSeconds {
		return Video{}, ErrNothingToJoin
	}
	if total := lenA + lenB; total > MaxJoinedSeconds+0.05 {
		return Video{}, &TooLongError{Seconds: total, Max: MaxJoinedSeconds}
	}
	w, h := c.joinSize(a, b)
	fps := math.Min(math.Max(frameRate(a), frameRate(b)), maxFPS)
	fit := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=bicubic,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%s",
		w, h, w, h, strconv.FormatFloat(fps, 'f', 3, 64))
	graph := []string{
		fmt.Sprintf("[0:%d]setpts=PTS-STARTPTS,trim=end=%s,%s,%s[v0]", a.videoIndex, seconds(lenA), c.videoFilters(ctx, a, "yuv420p"), fit),
		fmt.Sprintf("[1:%d]trim=start_frame=1,setpts=PTS-STARTPTS,%s,%s[v1]", b.videoIndex, c.videoFilters(ctx, b, "yuv420p"), fit),
	}
	sound := a.audio() || b.audio()
	if sound {
		graph = append(graph, joinSound(0, a, 0, lenA, "a0"), joinSound(1, b, skip, lenB, "a1"),
			"[v0][a0][v1][a1]concat=n=2:v=1:a=1[v][a]")
	} else {
		graph = append(graph, "[v0][v1]concat=n=2:v=1:a=0[v]")
	}
	out := filepath.Join(dir, "joined.mp4")
	pixels := strconv.FormatInt(c.maxPixels(), 10)
	args := []string{"-hide_banner", "-nostdin", "-v", "error",
		"-protocol_whitelist", "file", "-max_pixels", pixels, "-f", a.demuxer, "-i", "file:" + first,
		"-protocol_whitelist", "file", "-max_pixels", pixels, "-f", b.demuxer, "-i", "file:" + next,
		"-filter_complex", strings.Join(graph, ";"), "-map", "[v]",
		"-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-preset", "veryfast", "-crf", "20", "-maxrate", maxBitrate, "-bufsize", "16M",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}
	if sound {
		args = append(args, "-map", "[a]", "-c:a", "aac", "-b:a", "160k")
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-movflags", "+faststart", "-f", "mp4", "-y", "file:"+out)
	if _, err := c.runFor(ctx, c.videoTimeout(), c.ffmpeg(), args); err != nil {
		if ctx.Err() != nil {
			return Video{}, err
		}
		return Video{}, fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
	}
	got, err := c.readClip(ctx, out)
	if err != nil {
		return Video{}, fmt.Errorf("the clip framy joined: %w", err)
	}
	st, err := os.Stat(out)
	if err != nil {
		return Video{}, err
	}
	v := Video{Path: out, ContentType: "video/mp4", Bytes: st.Size(), Seconds: got.seconds, FPS: got.fps, Audio: got.audio()}
	v.Width, v.Height = got.shown()
	return v, nil
}

// joinSound is part n's sound for a join, length seconds of it from skip,
// as 48 kHz stereo: silence where the part has none.
func joinSound(n int, k clip, skip, length float64, label string) string {
	if !k.audio() {
		return fmt.Sprintf("anullsrc=r=48000:cl=stereo,atrim=end=%s[%s]", seconds(length), label)
	}
	return fmt.Sprintf("[%d:%d]atrim=start=%s,asetpts=PTS-STARTPTS,aresample=48000,aformat=channel_layouts=stereo,apad,atrim=end=%s[%s]",
		n, k.audioIndex, seconds(skip), seconds(length), label)
}

// joinSize is the larger of the two clips' sizes as shown, within the long
// side framy keeps, in even numbers.
func (c *Converter) joinSize(a, b clip) (int, int) {
	w, h := a.shown()
	if bw, bh := b.shown(); bw*bh > w*h {
		w, h = bw, bh
	}
	if long := c.videoLongSide(); max(w, h) > long {
		f := float64(long) / float64(max(w, h))
		w, h = int(math.Round(float64(w)*f)), int(math.Round(float64(h)*f))
	}
	return max(2, w&^1), max(2, h&^1)
}

// frameRate is a clip's frame rate, or joinFPS where it says none.
func frameRate(k clip) float64 {
	if k.fps > 0 {
		return k.fps
	}
	return joinFPS
}
