// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// scenes are the loops: the generating one and the album preview of albums of
// the given sizes.
func scenes(t *testing.T, sizes ...int) map[string]Scene {
	t.Helper()
	out := map[string]Scene{"generating": Generating{}}
	dir := t.TempDir()
	for _, n := range sizes {
		sub := filepath.Join(dir, itoa(n))
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		a, err := NewAlbum(context.Background(), album(t, sub, n, 4))
		if err != nil {
			t.Fatal(err)
		}
		out["album of "+itoa(n)] = a
	}
	return out
}

// The frame at the loop's length is the first frame again, so the step from
// the last frame drawn to the first is a step like any other.
func TestEveryLoopClosesOnItsFirstFrame(t *testing.T) {
	for name, s := range scenes(t, 1, 2, 5, 10) {
		first, last := frameAt(s, 0), frameAt(s, s.Duration())
		for i := range first.Pix {
			if first.Pix[i] != last.Pix[i] {
				t.Errorf("%s: frame at %gs differs from frame 0 at value %d (pixel %d, %d)",
					name, s.Duration(), i, i/3%first.W, i/3/first.W)
				break
			}
		}
	}
}

// The largest change between two frames, as a mean over the picture's bytes,
// stays where a calm loop keeps it. maply's slow loops peak at about 0.3 and
// 5 on this scale, and the generating loop's morphs at about 3.5; a cut or a
// fast draft is far above.
func TestLoopsMoveSlowly(t *testing.T) {
	limit := map[string]float64{"generating": 5}
	for name, s := range scenes(t, 1, 10) {
		peak := slices.Max(steps(s))
		want, ok := limit[name]
		if !ok {
			want = 10
		}
		if peak > want {
			t.Errorf("%s: the biggest step between frames is %.2f, more than %.1f", name, peak, want)
		}
		if peak < 0.05 {
			t.Errorf("%s: the loop does not move (%.3f)", name, peak)
		}
	}
}

// The generating loop shows the person that something is running, so it must
// keep moving the whole time, not only in a stretch of it.
func TestGeneratingNeverStandsStill(t *testing.T) {
	for i, d := range steps(Generating{}) {
		if d < 0.005 {
			t.Fatalf("frame %d is the same as the one before it", i+1)
		}
	}
}

// counter paints frame i in grey level i, so the raw stream shows the order.
type counter struct{}

func (counter) Size() (int, int)  { return 8, 4 }
func (counter) Duration() float64 { return 2 }
func (counter) Draw(c *Canvas, t float64) {
	// The frames dealt out together finish in reverse, so what is written
	// in the order of arrival would be written wrong.
	time.Sleep(time.Duration(7-int(math.Round(t*FPS))%7) * time.Millisecond)
	v := float32(math.Round(t*FPS)) / 255
	c.Fill(RGB{v, v, v})
}

// Frames drawn at once are written in order, every one of them.
func TestFramesComeInOrder(t *testing.T) {
	var out bytes.Buffer
	st, err := frames(context.Background(), counter{}, 7, &out)
	if err != nil {
		t.Fatal(err)
	}
	const size = 3 * 8 * 4
	if st.Frames != 60 || out.Len() != 60*size {
		t.Fatalf("%d frames, %d bytes", st.Frames, out.Len())
	}
	for i := range 60 {
		if got := out.Bytes()[i*size]; int(got) != i {
			t.Fatalf("frame %d holds frame %d", i, got)
		}
	}
}

// spy is an ffmpeg that records how it was started and reads the frames.
func spy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nfor a; do last=$a; done\nprintf '%s\\n' \"$@\" > \"$last.args\"\nenv > \"$last.env\"\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// ffmpeg reads people's photos' pixels, not their files, but it still runs
// with nothing of the service's environment and may open nothing but the
// pipe it is given.
func TestFFmpegRunsWithNothingAndReadsOnlyThePipe(t *testing.T) {
	t.Setenv("FRAMY_ANIM_LEAK", "secret")
	out := filepath.Join(t.TempDir(), "loop.mp4")
	if _, err := Encode(context.Background(), counter{}, out, Options{FFmpeg: spy(t, "cat > /dev/null\n: > \"$last\"\n")}); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(out + ".part.env")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"FRAMY_ANIM_LEAK", "HOME=", "USER="} {
		if strings.Contains(string(env), leak) {
			t.Errorf("ffmpeg inherited %s", leak)
		}
	}
	raw, err := os.ReadFile(out + ".part.args")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	has := func(seq ...string) bool {
		for i := 0; i+len(seq) <= len(args); i++ {
			if slices.Equal(args[i:i+len(seq)], seq) {
				return true
			}
		}
		return false
	}
	for _, want := range [][]string{
		{"-protocol_whitelist", "pipe", "-f", "rawvideo"}, // before the only input
		{"-i", "pipe:0"},
		{"-an"}, // a loop with a sound track is a video to Telegram, not a GIF
		{"-c:v", "libx264"},
		{"-pix_fmt", "yuv420p"},
		{"-movflags", "+faststart"},
		{"-f", "mp4", out + ".part"},
	} {
		if !has(want...) {
			t.Errorf("ffmpeg was not given %v; args: %v", want, args)
		}
	}
	if has("-nostdin") {
		t.Error("-nostdin would stop ffmpeg reading the frames")
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the finished file is not under its name: %v", err)
	}
}

type odd struct{ counter }

func (odd) Size() (int, int) { return 9, 4 }

// yuv420p halves the colour planes, so a side must be even.
func TestOddSizesAreRefused(t *testing.T) {
	_, err := Encode(context.Background(), odd{}, filepath.Join(t.TempDir(), "x.mp4"), Options{FFmpeg: "/nonexistent/ffmpeg"})
	if err == nil || !strings.Contains(err.Error(), "yuv420p") {
		t.Errorf("a loop 9 pixels wide: %v", err)
	}
}

// A failed or stopped encoding leaves no file, whole or part, and says why.
// Each ffmpeg here has begun to write its output before it fails.
func TestAFailedEncodingLeavesNothing(t *testing.T) {
	const partial = "echo half a file > \"$last\"\n"
	out := filepath.Join(t.TempDir(), "loop.mp4")
	_, err := Encode(context.Background(), counter{}, out, Options{FFmpeg: spy(t, partial+"cat > /dev/null\necho 'No such encoder' >&2\nexit 1\n")})
	if err == nil || !strings.Contains(err.Error(), "No such encoder") {
		t.Errorf("ffmpeg's complaint is lost: %v", err)
	}
	// ffmpeg that gives up before it has read a frame: the writes meet a
	// closed pipe, and what it said is still what the error says.
	_, err = Encode(context.Background(), counter{}, out, Options{FFmpeg: spy(t, partial+"echo 'Unrecognized option' >&2\nexit 1\n")})
	if err == nil || !strings.Contains(err.Error(), "Unrecognized option") {
		t.Errorf("ffmpeg that quits at once: %v", err)
	}
	if _, err := Encode(context.Background(), counter{}, out, Options{FFmpeg: "/nonexistent/ffmpeg"}); err == nil {
		t.Error("a missing ffmpeg is not an error")
	}
	// A stuck ffmpeg is stopped by the timeout, not waited for.
	start := time.Now()
	_, err = Encode(context.Background(), counter{}, out, Options{FFmpeg: spy(t, partial+"exec sleep 30\n"), Timeout: 200 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Errorf("a stuck ffmpeg after %s: %v", time.Since(start), err)
	}
	// So is one whose caller gives up.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err = Encode(ctx, counter{}, out, Options{FFmpeg: spy(t, partial+"exec sleep 30\n")}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled encoding: %v", err)
	}
	for _, name := range []string{out, out + ".part"} {
		if _, err := os.Stat(name); err == nil {
			t.Errorf("left behind: %s", name)
		}
	}
}

// The dots take every figure in turn: once its morph is over every dot is
// as full as the figure says, the figure covers a fair part of the grid and
// not most of it, and each differs from the one before it in many dots.
func TestGeneratingTakesEveryFigure(t *testing.T) {
	grid := func(f func(px, py float64) bool) []bool {
		var in []bool
		for y := -240.0; y <= 240; y += dotPitch {
			for x := -560.0; x <= 560; x += dotPitch {
				in = append(in, f(x, y))
			}
		}
		return in
	}
	var prev []bool
	for k, f := range append(figures, figures[0]) {
		at := (float64(k) + 0.8) * shapeFor
		th := 2 * math.Pi * math.Mod(at, generatingDur) / generatingDur
		wrong := 0
		in := grid(func(px, py float64) bool {
			want := smooth(-dotPitch*0.4, dotPitch*1.1, -shapeAt(k%len(figures), px, py, th))
			if got := dotAt(0, 0, figX+px, figY+py, at); math.Abs(got-want) > 1e-9 {
				wrong++
			}
			return want > 0.5
		})
		if wrong > 0 {
			t.Errorf("%s at %.2fs: %d dots are not the figure's yet", f.name, at, wrong)
		}
		full := 0
		for _, v := range in {
			if v {
				full++
			}
		}
		if share := float64(full) / float64(len(in)); share < 0.04 || share > 0.45 {
			t.Errorf("%s fills %.0f%% of the grid", f.name, 100*share)
		}
		if prev != nil {
			differ := 0
			for i := range in {
				if in[i] != prev[i] {
					differ++
				}
			}
			if differ < len(in)/20 {
				t.Errorf("%s differs from the figure before it in only %d dots of %d", f.name, differ, len(in))
			}
		}
		prev = in
	}
	// The frame says the same: the blob's middle is a full white dot, and a
	// dot in a corner of the box is a small pale one.
	c := frameAt(Generating{}, 0.8*shapeFor)
	if got := rgb(c, int(figX), int(figY)); !near(got, [3]int{255, 255, 255}, 2) {
		t.Errorf("the middle of the blob is %v, not white", got)
	}
	if got := rgb(c, int(figX)-560, int(figY)-240); !near(got, rgbOf(haze), 2) {
		t.Errorf("a dot in the corner is %v, not the pale grain %v", got, rgbOf(haze))
	}
}
