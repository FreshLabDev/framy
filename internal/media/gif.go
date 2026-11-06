// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// GIFVideo writes the GIF at dir/In as a silent MP4 next to it, the clip
// Telegram plays an animation as: H.264 in yuv420p with even sides, its
// index at the front, every frame as long as the GIF shows it. framy gives
// Telegram a GIF this way where Telegram fetches it by a link (an inline
// result, a guest answer), since a link to an animation has to be an MP4;
// a GIF uploaded to Telegram it turns into the same clip itself. A file that
// is no GIF is ErrUnsupportedVideo, one ffmpeg cannot read
// ErrUnreadableVideo.
func (c *Converter) GIFVideo(ctx context.Context, dir string) (Video, error) {
	in := filepath.Join(dir, In)
	head, err := readHead(in)
	if err != nil {
		return Video{}, err
	}
	if Sniff(head) != GIF {
		return Video{}, ErrUnsupportedVideo
	}
	out := filepath.Join(dir, "out.mp4")
	// A GIF has one stream, of pictures in SDR with no rotation.
	k := clip{demuxer: demuxer[GIF], videoIndex: 0, audioIndex: -1}
	args := []string{
		"-hide_banner", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
		"-max_pixels", strconv.FormatInt(c.maxPixels(), 10),
		"-threads", clipThreads, "-f", k.demuxer, "-i", "file:" + in, "-map", "0:v:0",
		"-vf", c.videoFilters(ctx, k, "yuv420p"),
		// Each frame keeps its own time: a GIF's frames may differ in length.
		"-fps_mode", "vfr",
		"-threads", clipThreads, "-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-preset", "veryfast", "-crf", "20", "-maxrate", maxBitrate, "-bufsize", "16M",
		"-an", "-map_metadata", "-1", "-movflags", "+faststart", "-f", "mp4", "-y", "file:" + out,
	}
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
	w, h := got.shown()
	return Video{Path: out, ContentType: "video/mp4", Width: w, Height: h, Seconds: got.seconds, FPS: got.fps, Bytes: st.Size()}, nil
}
