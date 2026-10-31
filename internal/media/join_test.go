// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// joinDir puts the clips made in first and next (as In) where Join reads
// them.
func joinDir(t *testing.T, first, next string) string {
	t.Helper()
	dir := t.TempDir()
	for from, to := range map[string]string{first: JoinFirst, next: JoinNext} {
		b, err := os.ReadFile(filepath.Join(from, In))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, to), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// asInput puts a clip where LastFrame and Poster read it.
func asInput(t *testing.T, path string) string {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, In), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func redClip(t *testing.T, size, secs string) string {
	t.Helper()
	return makeClip(t, "-f", "lavfi", "-i", "color=c=red:s="+size+":r=24:d="+secs,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
}

func TestJoin(t *testing.T) {
	c := videoConverter(t)
	ctx := context.Background()
	// 2 s of the test pattern with a tone, then 1 s of red at a larger size,
	// with no sound and another frame rate.
	v, err := c.Join(ctx, joinDir(t, h264(t, "320x180", "2"), redClip(t, "640x360", "1")), 0)
	if err != nil {
		t.Fatal(err)
	}
	if v.Width != 640 || v.Height != 360 || v.ContentType != "video/mp4" || !v.Audio {
		t.Errorf("joined: %+v", v)
	}
	// The next part loses its first frame, the one it started from.
	if want := 2 + 1 - 1.0/24; math.Abs(v.Seconds-want) > 0.1 {
		t.Errorf("the joined clip is %.3f s, not %.3f", v.Seconds, want)
	}
	poster, err := c.Poster(ctx, asInput(t, v.Path))
	if err != nil {
		t.Fatal(err)
	}
	if flat(readPNG(t, poster)) {
		t.Error("the joined clip does not begin with the first clip")
	}
	last, _, err := c.LastFrame(ctx, asInput(t, v.Path))
	if err != nil {
		t.Fatal(err)
	}
	if px := readPNG(t, last).At(320, 180); !near(px, "fe0000") {
		t.Errorf("the joined clip ends on %s, not red", hex(px))
	}
}

func TestJoinStopsAtTheFrame(t *testing.T) {
	c := videoConverter(t)
	// The frame was taken at 1 s, as before a fade: the clip goes up to it.
	v, err := c.Join(context.Background(), joinDir(t, h264(t, "320x180", "2"), redClip(t, "320x180", "1")), 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + 1.0/30 + 1 - 1.0/24; math.Abs(v.Seconds-want) > 0.1 {
		t.Errorf("the joined clip is %.3f s, not %.3f", v.Seconds, want)
	}
}

func TestJoinWithoutSound(t *testing.T) {
	c := videoConverter(t)
	// A square part after a wide clip of the same area keeps the first
	// clip's size, the square padded.
	v, err := c.Join(context.Background(), joinDir(t, redClip(t, "320x180", "1"), redClip(t, "240x240", "1")), 0)
	if err != nil {
		t.Fatal(err)
	}
	if v.Audio || v.Width != 320 || v.Height != 180 {
		t.Errorf("joined: %+v", v)
	}
}

func TestJoinRefusesALongClip(t *testing.T) {
	c := videoConverter(t)
	long := makeClip(t, "-f", "lavfi", "-i", "color=c=blue:s=32x32:r=1:d=179",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
	_, err := c.Join(context.Background(), joinDir(t, long, redClip(t, "32x32", "2")), 0)
	var tooLong *TooLongError
	if !errors.As(err, &tooLong) || tooLong.Max != MaxJoinedSeconds {
		t.Fatalf("a join over %d s: %v", MaxJoinedSeconds, err)
	}
	// A next part of a single frame has nothing to add.
	one := makeClip(t, "-f", "lavfi", "-i", "color=c=red:s=32x32:r=24:d=0.04",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
	if _, err := c.Join(context.Background(), joinDir(t, redClip(t, "32x32", "1"), one), 0); !errors.Is(err, ErrNothingToJoin) {
		t.Errorf("a single frame after the clip: %v", err)
	}
}
