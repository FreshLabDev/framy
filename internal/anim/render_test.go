// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What Telegram plays as an animation: H.264 in an MP4 with no sound track
// (sendAnimation, up to 50 MB; docs/research/telegram.md, 3.2), and what
// clients decode without trouble: yuv420p, even sides, a modest size.
func TestLoopsGoOutAsTelegramAnimations(t *testing.T) {
	for name, m := range map[string]Media{"album preview": sharedAlbum(t), "generating loop": sharedGenerating(t)} {
		p := probe(t, m.Path)
		if len(p.Streams) != 1 || p.Streams[0].CodecType != "video" {
			t.Errorf("%s: streams %+v; a sound track turns it into a video", name, p.Streams)
			continue
		}
		s := p.Streams[0]
		if s.CodecName != "h264" || s.PixFmt != "yuv420p" {
			t.Errorf("%s: %s in %s, want h264 in yuv420p", name, s.CodecName, s.PixFmt)
		}
		if s.Width != m.Width || s.Height != m.Height || s.Width%2 != 0 || s.Height%2 != 0 || max(s.Width, s.Height) > 1280 {
			t.Errorf("%s: %d×%d, said to be %d×%d", name, s.Width, s.Height, m.Width, m.Height)
		}
		if s.RFrameRate != strconv.Itoa(FPS)+"/1" {
			t.Errorf("%s: %s frames a second", name, s.RFrameRate)
		}
		secs, _ := strconv.ParseFloat(p.Format.Duration, 64)
		frames, _ := strconv.Atoi(s.NbFrames)
		if math.Abs(secs-m.Duration.Seconds()) > 0.05 || frames != int(math.Round(m.Duration.Seconds()*FPS)) {
			t.Errorf("%s: %d frames and %.3f s, said to be %s", name, frames, secs, m.Duration)
		}
		if m.Duration < time.Second || m.Duration > 15*time.Second {
			t.Errorf("%s: %s is not a short loop", name, m.Duration)
		}
		st, err := os.Stat(m.Path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() != m.Bytes || m.Bytes > MaxAnimationBytes || m.Bytes > 50<<20 {
			t.Errorf("%s: %d bytes on disk, said to be %d, budget %d", name, st.Size(), m.Bytes, MaxAnimationBytes)
		}
		if !strings.Contains(p.Format.Name, "mp4") || m.ContentType != "video/mp4" {
			t.Errorf("%s: %s, %s", name, p.Format.Name, m.ContentType)
		}
		// +faststart: the index comes first, so a client plays while it downloads.
		boxes := topBoxes(t, m.Path)
		if i, j := slices.Index(boxes, "moov"), slices.Index(boxes, "mdat"); i < 0 || j < 0 || i > j {
			t.Errorf("%s: boxes %v, moov must come before mdat", name, boxes)
		}
		t.Logf("%s: %s %s %d×%d, %.1f s, %d frames, %d KB", name, s.CodecName, s.PixFmt, s.Width, s.Height, secs, frames, m.Bytes/1024)
	}
}

// The video that goes out is the loop that was drawn: its frames are the
// scene's frames, in order and in the same colours, and the step from its
// last frame to its first is no bigger than the drawn loop's own plus the
// noise of the codec.
//
// The colours are checked on the plain field under the viewfinder, where
// yuv420p loses nothing: it must come back within 2 levels, and a wrong
// colour matrix moves it by up to 8, a wrong range by up to 10. The whole
// frame is within 3 levels of the drawn one, on average over its bytes:
// yuv420p keeps one colour for every 2×2 pixels, and at the edges of the
// photos and the dots that costs about 2 with the ffmpeg in the image,
// whatever the quality.
func TestTheVideoIsTheDrawnLoop(t *testing.T) {
	sharedAlbum(t)
	a, err := NewAlbum(context.Background(), shared.photos)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		m     Media
		scene Scene
	}{{"album preview", sharedAlbum(t), a}, {"generating loop", sharedGenerating(t), Generating{}}} {
		frames := decodeFrames(t, c.m.Path, c.m.Width, c.m.Height)
		n := Frames(c.scene)
		if len(frames) != n {
			t.Fatalf("%s: %d frames decoded, %d drawn", c.name, len(frames), n)
		}
		drawn := func(i int) []byte {
			cv := NewCanvas(c.m.Width, c.m.Height)
			c.scene.Draw(cv, float64(i)/FPS)
			b := make([]byte, 3*cv.W*cv.H)
			cv.RGB24(b)
			return b
		}
		for _, i := range []int{0, n / 4, n / 2, 2 * n / 3, n - 1} {
			if d := meanDiff(frames[i], drawn(i)); d > 3 {
				t.Errorf("%s: frame %d is %.2f from the frame that was drawn", c.name, i, d)
			}
			if got, want := fieldBelow(frames[i], c.m.Width), rgbOf(Field); !near(got, want, 2) {
				t.Errorf("%s: frame %d shows the field as %v, drawn %v", c.name, i, got, want)
			}
		}
		seam := meanDiff(frames[n-1], frames[0])
		own := meanDiff(drawn(n-1), drawn(0))
		if seam > own+1.5 {
			t.Errorf("%s: the last frame is %.3f from the first; the drawn loop's own step is %.3f", c.name, seam, own)
		}
		t.Logf("%s: last to first %.3f, drawn %.3f", c.name, seam, own)
	}
}

// The generating loop is drawn once and kept under its key; a new design is
// a new key, a new file, and the old file goes.
func TestGeneratingIsDrawnOnceAndKeyedByDesign(t *testing.T) {
	needFFmpeg(t)
	r := &Renderer{Cache: t.TempDir()}
	ctx := context.Background()
	first, err := r.Generating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Key == "" || first.Key != r.GeneratingKey() || !strings.Contains(first.Path, first.Key) {
		t.Errorf("key %q, path %s", first.Key, first.Path)
	}
	st1, _ := os.Stat(first.Path)
	again, err := r.Generating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(again.Path)
	if again != first || !os.SameFile(st1, st2) {
		t.Error("the loop was drawn again")
	}

	// Callers that arrive together share one drawing.
	done := make(chan Media, 4)
	for range 4 {
		go func() {
			m, _ := r.Generating(ctx)
			done <- m
		}()
	}
	for range 4 {
		if m := <-done; m != first {
			t.Errorf("a concurrent call got %+v", m)
		}
	}

	old := loopVersion
	t.Cleanup(func() { loopVersion = old })
	loopVersion++
	next, err := r.Generating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next.Key == first.Key || next.Path == first.Path {
		t.Error("a new design has the old key")
	}
	if _, err := os.Stat(first.Path); err == nil {
		t.Error("the loop of the old design is still there")
	}
	files, _ := filepath.Glob(filepath.Join(r.Cache, "*"))
	if len(files) != 1 || files[0] != next.Path {
		t.Errorf("the cache holds %v", files)
	}
}

// The key follows everything the picture is made of.
func TestGeneratingKeyFollowsTheDesign(t *testing.T) {
	base := generatingKey(Options{})
	oldV, oldField := loopVersion, Field
	t.Cleanup(func() { loopVersion, Field = oldV, oldField })
	loopVersion++
	if generatingKey(Options{}) == base {
		t.Error("the key ignores the design's version")
	}
	loopVersion = oldV
	Field = Hex("#123456")
	if generatingKey(Options{}) == base {
		t.Error("the key ignores the field colour")
	}
	Field = oldField
	if generatingKey(Options{CRF: 30}) == base || generatingKey(Options{Preset: "slow"}) == base {
		t.Error("the key ignores the encoding")
	}
	if generatingKey(Options{Workers: 3, FFmpeg: "x"}) != base {
		t.Error("the key follows what does not change the picture")
	}
}

// The renderer takes one job at a time and gives up on a cancelled context
// while it waits.
func TestRendererQueuesAndHonoursTheContext(t *testing.T) {
	r := &Renderer{}
	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Album(ctx, t.TempDir(), []string{"a.jpg"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a call that waits: %v", err)
	}
	release()
	if _, err := r.Album(context.Background(), t.TempDir(), nil); !errors.Is(err, ErrNoPhotos) {
		t.Errorf("no photos: %v", err)
	}
}

// Photos of a phone's size, ten of them: what the user waits for. The numbers
// are logged; the bound only catches a render that has gone wrong.
func TestRenderTimes(t *testing.T) {
	r := needFFmpeg(t)
	dir := t.TempDir()
	// The race detector makes drawing ten times slower, and the numbers
	// would say nothing: under it the photos are a quarter of the size.
	shrink := 1
	if raceOn {
		shrink = 4
	}
	paths := album(t, dir, 10, shrink)
	out := t.TempDir()
	start := time.Now()
	m, err := r.Album(context.Background(), out, paths)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("album preview, 10 photos of up to 2048 px: %s, %d KB", took.Round(time.Millisecond), m.Bytes/1024)
	if took > time.Minute {
		t.Errorf("the album preview took %s", took)
	}
	start = time.Now()
	g, err := (&Renderer{Cache: t.TempDir()}).Generating(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("generating loop: %s, %d KB", time.Since(start).Round(time.Millisecond), g.Bytes/1024)
	start = time.Now()
	c, err := (&Renderer{Cache: t.TempDir()}).ErrorCard(context.Background(), "The model refused this picture. Try other words.", "en")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("error card: %s, %d KB", time.Since(start).Round(time.Millisecond), c.Bytes/1024)
	if m.Bytes > MaxAnimationBytes {
		t.Errorf("%d bytes for ten photos", m.Bytes)
	}
}

func BenchmarkAlbumOfTen(b *testing.B) {
	r := &Renderer{}
	if err := r.Check(context.Background()); err != nil {
		b.Skip(err)
	}
	paths := album(b, b.TempDir(), 10, 1)
	out := b.TempDir()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Album(context.Background(), out, paths); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAlbumOfOne(b *testing.B) {
	r := &Renderer{}
	if err := r.Check(context.Background()); err != nil {
		b.Skip(err)
	}
	paths := album(b, b.TempDir(), 1, 1)
	out := b.TempDir()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Album(context.Background(), out, paths); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerating(b *testing.B) {
	r := &Renderer{}
	if err := r.Check(context.Background()); err != nil {
		b.Skip(err)
	}
	out := filepath.Join(b.TempDir(), "generating.mp4")
	b.ResetTimer()
	for b.Loop() {
		if _, err := Encode(context.Background(), Generating{}, out, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkErrorCard(b *testing.B) {
	text := "The model refused this picture. Try other words."
	for i := 0; b.Loop(); i++ {
		r := &Renderer{Cache: b.TempDir()}
		if _, err := r.ErrorCard(context.Background(), text, "en"); err != nil {
			b.Fatal(err)
		}
	}
}

// One frame of the album in the middle of its rise, where every photo is
// sampled: what a core does in a frame.
func BenchmarkAlbumFrame(b *testing.B) {
	a, err := NewAlbum(context.Background(), album(b, b.TempDir(), 10, 3))
	if err != nil {
		b.Fatal(err)
	}
	c := NewCanvas(Side, Side)
	t := albumDur / 4
	b.ResetTimer()
	for b.Loop() {
		a.Draw(c, t)
	}
}

// fieldBelow is the mean colour of the strip of plain field under the
// viewfinder in a frame of raw RGB, w pixels wide.
func fieldBelow(frame []byte, w int) [3]int {
	var sum [3]int
	n := 0
	for y := frameY1 + 8; y < frameY1+28; y++ {
		for x := 100; x < w-100; x++ {
			for k := range 3 {
				sum[k] += int(frame[3*(y*w+x)+k])
			}
			n++
		}
	}
	return [3]int{(sum[0] + n/2) / n, (sum[1] + n/2) / n, (sum[2] + n/2) / n}
}
