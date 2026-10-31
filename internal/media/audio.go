// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

// Sounds (docs/design.md, Storage and retention). A sound goes to a video
// model as a reference. framy keeps an MP3 as it came and anything else as
// a 16-bit WAV at 48 kHz, in stereo at most. The tags go, as a clip's do.

// The demuxers framy reads a sound with, besides a clip's, which read an M4A
// and a WebM of sound alone.
const (
	demuxMP3  = "mp3"
	demuxWAV  = "wav"
	demuxOgg  = "ogg"
	demuxFLAC = "flac"
	demuxAAC  = "aac"
)

// errNoVideo: a clip with no picture, which may be a sound.
var errNoVideo = fmt.Errorf("%w: no video in the file", ErrUnsupportedVideo)

// SniffAudio names the demuxer of a sound from its first bytes: an MP3, a
// WAV, an Ogg, a FLAC or an ADTS stream of AAC, or "" for anything else. An
// M4A and a WebM of sound are clips to SniffVideo, and ffprobe tells them
// apart.
func SniffAudio(head []byte) string {
	switch {
	case len(head) >= 3 && string(head[:3]) == "ID3":
		return demuxMP3
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return demuxWAV
	case len(head) >= 4 && string(head[:4]) == "OggS":
		return demuxOgg
	case len(head) >= 4 && string(head[:4]) == "fLaC":
		return demuxFLAC
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xF6 == 0xF0:
		// MPEG sync with layer 0: an ADTS frame.
		return demuxAAC
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 && (head[1]>>1)&3 != 0:
		return demuxMP3
	}
	return ""
}

// sound is what ffprobe says of a sound.
type sound struct {
	demuxer  string
	index    int
	codec    string
	channels int
	seconds  float64
}

func (c *Converter) probeSound(ctx context.Context, demuxer, in string) (sound, error) {
	raw, err := c.run(ctx, c.ffprobe(), []string{
		"-hide_banner", "-v", "error",
		"-protocol_whitelist", "file",
		"-f", demuxer,
		"-show_streams", "-show_format", "-of", "json", "file:" + in,
	})
	if err != nil {
		if ctx.Err() != nil {
			return sound{}, err
		}
		return sound{}, fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
	}
	var out videoProbeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return sound{}, fmt.Errorf("%w: ffprobe: %v", ErrUnreadableVideo, err)
	}
	k := sound{demuxer: demuxer, index: -1}
	for _, s := range out.Streams {
		switch {
		case s.CodecType == "video" && s.Disposition.AttachedPic == 0 && s.Width > 0 && s.Height > 0:
			// A clip with a picture is a video, whatever else it has.
			return sound{}, fmt.Errorf("%w: a video, not a sound", ErrUnsupportedVideo)
		case s.CodecType == "audio" && k.index < 0:
			k.index, k.codec, k.channels, k.seconds = s.Index, s.CodecName, s.Channels, number(s.Duration)
		}
	}
	if k.index < 0 {
		return sound{}, fmt.Errorf("%w: no sound in the file", ErrUnsupportedVideo)
	}
	if d := number(out.Format.Duration); d > 0 {
		k.seconds = d
	}
	if k.seconds <= 0 {
		return sound{}, fmt.Errorf("%w: the sound has no length", ErrUnreadableVideo)
	}
	return k, nil
}

// NormalizeClip makes the clip at dir/In, a video or a sound, or the part
// cut of it, into what framy keeps: NormalizeVideo for a video and
// NormalizeAudio for a sound, with their errors.
func (c *Converter) NormalizeClip(ctx context.Context, dir string, cut *Cut) (Video, error) {
	head, err := readHead(filepath.Join(dir, In))
	if err != nil {
		return Video{}, err
	}
	if SniffAudio(head) != "" {
		return c.NormalizeAudio(ctx, dir, cut)
	}
	v, err := c.NormalizeVideo(ctx, dir, cut)
	if errors.Is(err, errNoVideo) {
		return c.NormalizeAudio(ctx, dir, cut)
	}
	return v, err
}

// NormalizeAudio makes the sound at dir/In, or the part cut of it, into what
// framy keeps, written next to it: an MP3 as it came when it is not cut, any
// other a WAV. Its errors are NormalizeVideo's.
func (c *Converter) NormalizeAudio(ctx context.Context, dir string, cut *Cut) (Video, error) {
	in := filepath.Join(dir, In)
	head, err := readHead(in)
	if err != nil {
		return Video{}, err
	}
	demuxer := SniffAudio(head)
	if demuxer == "" {
		demuxer = SniffVideo(head)
	}
	if demuxer == "" {
		return Video{}, ErrUnsupportedVideo
	}
	k, err := c.probeSound(ctx, demuxer, in)
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
	if length > c.maxSeconds()+0.05 {
		return Video{}, &TooLongError{Seconds: length, Max: c.maxSeconds()}
	}
	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-protocol_whitelist", "file"}
	if cut != nil {
		args = append(args, "-ss", seconds(start), "-t", seconds(length))
	}
	args = append(args, "-f", k.demuxer, "-i", "file:"+in, "-map", "0:"+strconv.Itoa(k.index), "-map_metadata", "-1")
	v := Video{Audio: true}
	out := filepath.Join(dir, "out.wav")
	format := demuxWAV
	if cut == nil && k.demuxer == demuxMP3 && k.codec == "mp3" {
		v.Copied, v.ContentType, out, format = true, "audio/mpeg", filepath.Join(dir, "out.mp3"), demuxMP3
		args = append(args, "-c:a", "copy", "-id3v2_version", "0")
	} else {
		v.ContentType = "audio/wav"
		args = append(args, "-c:a", "pcm_s16le", "-ar", "48000")
		if k.channels > 2 {
			args = append(args, "-ac", "2")
		}
	}
	args = append(args, "-f", format, "-y", "file:"+out)
	if _, err := c.runFor(ctx, c.videoTimeout(), c.ffmpeg(), args); err != nil {
		if ctx.Err() != nil {
			return Video{}, err
		}
		return Video{}, fmt.Errorf("%w: %v", ErrUnreadableVideo, err)
	}
	got, err := c.probeSound(ctx, format, out)
	if err != nil {
		return Video{}, fmt.Errorf("the sound framy wrote: %w", err)
	}
	st, err := os.Stat(out)
	if err != nil {
		return Video{}, err
	}
	v.Path, v.Bytes, v.Seconds = out, st.Size(), got.seconds
	return v, nil
}
