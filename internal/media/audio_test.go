// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"strings"
	"testing"
)

func TestSniffAudio(t *testing.T) {
	for _, c := range []struct {
		name string
		head []byte
		want string
	}{
		{"mp3 with tags", []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), "mp3"},
		{"mp3 frame", []byte{0xFF, 0xFB, 0x90, 0x64}, "mp3"},
		{"wav", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "wav"},
		{"ogg", []byte("OggS\x00\x02"), "ogg"},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), "flac"},
		{"adts", []byte{0xFF, 0xF1, 0x50, 0x80}, "aac"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, ""},
		{"riff that is no wav", []byte("RIFF\x24\x00\x00\x00AVI LIST"), ""},
		{"mp4", []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00"), ""},
		{"empty", nil, ""},
	} {
		if got := SniffAudio(c.head); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// tone is a sound of secs seconds made by ffmpeg with the encoder and the
// format, in channels; the test is skipped where this ffmpeg lacks them.
func tone(t *testing.T, secs, channels, codec, format string) string {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil || !strings.Contains(string(out), " "+codec+" ") {
		t.Skipf("this ffmpeg has no %s encoder", codec)
	}
	return makeClip(t, "-f", "lavfi", "-i", "sine=f=440:d="+secs, "-ac", channels,
		"-metadata", "title=a private name", "-c:a", codec, "-f", format)
}

// soundOf is what ffprobe says of the sound at path: its codec, channels,
// sample rate, length and tags.
func soundOf(t *testing.T, path string) (codec string, channels int, rate string, secs float64, tags map[string]string) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			Channels   int    `json:"channels"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil || len(p.Streams) != 1 {
		t.Fatalf("%s: %s %v", path, out, err)
	}
	s := p.Streams[0]
	return s.CodecName, s.Channels, s.SampleRate, number(p.Format.Duration), p.Format.Tags
}

// An MP3 is kept as it came, without its tags; any other sound becomes a
// WAV at 48 kHz in stereo at most. A clip with a picture is a video still.
func TestASoundIsKeptAsAnMP3OrAWAV(t *testing.T) {
	c := videoConverter(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name               string
		dir                func() string
		contentType, codec string
		channels           int
		copied             bool
	}{
		{"mp3", func() string { return tone(t, "3", "2", "libmp3lame", "mp3") }, "audio/mpeg", "mp3", 2, true},
		{"m4a", func() string { return tone(t, "3", "1", "aac", "ipod") }, "audio/wav", "pcm_s16le", 1, false},
		{"flac in six channels", func() string { return tone(t, "3", "6", "flac", "flac") }, "audio/wav", "pcm_s16le", 2, false},
		{"adts", func() string { return tone(t, "3", "2", "aac", "adts") }, "audio/wav", "pcm_s16le", 2, false},
		{"opus in ogg", func() string { return tone(t, "3", "2", "libopus", "ogg") }, "audio/wav", "pcm_s16le", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := c.NormalizeClip(ctx, tc.dir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if v.ContentType != tc.contentType || v.Copied != tc.copied || !v.Audio || v.Width != 0 || math.Abs(v.Seconds-3) > 0.1 || v.Bytes == 0 {
				t.Errorf("kept %+v", v)
			}
			codec, channels, rate, secs, tags := soundOf(t, v.Path)
			if codec != tc.codec || channels != tc.channels || (!tc.copied && rate != "48000") || math.Abs(secs-3) > 0.1 {
				t.Errorf("%s, %d channels, %s Hz, %.3f s", codec, channels, rate, secs)
			}
			if tags["title"] != "" {
				t.Errorf("the tags stayed: %v", tags)
			}
		})
	}

	// A clip with a picture and a sound is a video.
	v, err := c.NormalizeClip(ctx, h264(t, "320x180", "1"), nil)
	if err != nil || v.ContentType != "video/mp4" || v.Width != 320 {
		t.Fatalf("a video: %+v %v", v, err)
	}
	// NormalizeVideo still takes no sound.
	if _, err := c.NormalizeVideo(ctx, tone(t, "1", "2", "aac", "ipod"), nil); !errors.Is(err, ErrUnsupportedVideo) {
		t.Errorf("a sound as a video: %v", err)
	}
}

// A part of a sound is cut exactly and kept as a WAV, an MP3's too; a sound
// over the limit asks for a part.
func TestAPartOfASoundIsCut(t *testing.T) {
	c := videoConverter(t)
	ctx := context.Background()
	v, err := c.NormalizeAudio(ctx, tone(t, "5", "2", "libmp3lame", "mp3"), &Cut{Start: 1, Seconds: 2.5})
	if err != nil {
		t.Fatal(err)
	}
	if v.ContentType != "audio/wav" || math.Abs(v.Seconds-2.5) > 0.05 {
		t.Errorf("the part %+v", v)
	}
	if _, err := c.NormalizeAudio(ctx, tone(t, "2", "2", "pcm_s16le", "wav"), &Cut{Start: 3, Seconds: 1}); !errors.Is(err, ErrBadCut) {
		t.Errorf("a part past the end: %v", err)
	}
	short := &Converter{MaxSeconds: 2}
	var long *TooLongError
	if _, err := short.NormalizeAudio(ctx, tone(t, "3", "2", "pcm_s16le", "wav"), nil); !errors.As(err, &long) || long.Max != 2 {
		t.Errorf("a sound over the limit: %v", err)
	}
	// A picture is no sound.
	dir := makeClip(t, "-f", "lavfi", "-i", "testsrc2=s=64x36:d=0.2", "-frames:v", "1", "-f", "image2", "-c:v", "png")
	if _, err := c.NormalizeAudio(ctx, dir, nil); !errors.Is(err, ErrUnsupportedVideo) {
		t.Errorf("a picture as a sound: %v", err)
	}
}
