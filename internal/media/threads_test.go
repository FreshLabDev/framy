// SPDX-License-Identifier: Apache-2.0
package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// spyFFmpeg is an ffmpeg that writes its arguments to a log, one per line
// and a line "--" after each run, and then runs the real one.
func spyFFmpeg(t *testing.T) (path string, runs func() [][]string) {
	t.Helper()
	real, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	log, path := filepath.Join(dir, "args"), filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> '" + log + "'\necho -- >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, func() [][]string {
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var out [][]string
		var run []string
		for _, a := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			if a == "--" {
				out = append(out, run)
				run = nil
				continue
			}
			run = append(run, a)
		}
		return out
	}
}

// boundedThreads says what of a run of ffmpeg is left at ffmpeg's own
// number of threads: an input with no -threads among its options, or an
// H.264 encoder with none after the last input.
func boundedThreads(args []string) []string {
	var missing []string
	from := 0
	last := -1
	for i, a := range args {
		if a != "-i" {
			continue
		}
		if !slices.Contains(args[from:i], "-threads") {
			missing = append(missing, "the input "+args[i+1])
		}
		from, last = i+2, i
	}
	if x := slices.Index(args, "libx264"); x > 0 && !slices.Contains(args[last+1:x], "-threads") {
		missing = append(missing, "the H.264 encoder")
	}
	return missing
}

// ffmpeg gives every decoder and encoder a thread per core, and each tile
// of a HEIC is a decoder of its own: a 12 MP photo from an iPhone started
// over 400 threads, past the container's pids_limit. Every ffmpeg framy
// starts on a picture, a clip or a sound says how many threads each input
// and the H.264 encoder get.
func TestEveryFFmpegSaysHowManyThreads(t *testing.T) {
	c := videoConverter(t)
	spy, runs := spyFFmpeg(t)
	c.FFmpeg = spy
	ctx := context.Background()

	if _, err := normalize(t, c, fixture(t, "iphone.heic")); err != nil {
		t.Fatal(err)
	}
	if c.CheckThumb(ctx) == nil {
		dir := t.TempDir()
		if _, _, err := c.Thumb(ctx, filepath.Join("testdata", "iphone.heic"), filepath.Join(dir, "thumb.webp")); err != nil {
			t.Fatal(err)
		}
	}
	// Encoded again: MPEG-4 Part 2 is not kept.
	clip := makeClip(t, "-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=1", "-c:v", "mpeg4", "-f", "matroska")
	v, err := c.NormalizeVideo(ctx, clip, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Copied {
		t.Fatal("the clip was copied, so nothing was encoded")
	}
	if _, _, err := c.LastFrame(ctx, asInput(t, v.Path)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Join(ctx, joinDir(t, h264(t, "320x180", "1"), redClip(t, "320x180", "1")), 0); err != nil {
		t.Fatal(err)
	}
	// A FLAC's decoder takes a thread per core as well.
	if _, err := c.NormalizeAudio(ctx, tone(t, "1", "2", "flac", "flac"), nil); err != nil {
		t.Fatal(err)
	}

	decoded, encoded := 0, 0
	for _, args := range runs() {
		if !slices.Contains(args, "-i") {
			continue // -encoders, -filters and the rest read no file
		}
		decoded++
		if slices.Contains(args, "libx264") {
			encoded++
		}
		if missing := boundedThreads(args); len(missing) > 0 {
			t.Errorf("ffmpeg %s\nleaves %s at a thread per core", strings.Join(args, " "), strings.Join(missing, " and "))
		}
	}
	if decoded < 5 || encoded < 2 {
		t.Errorf("the spy saw %d runs that read a file and %d that encoded H.264", decoded, encoded)
	}
}
