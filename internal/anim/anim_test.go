// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

// What the tests share: ten photos, the album preview made of them and the
// generating loop, each drawn and encoded once, since encoding is the slow
// part of a test run.
var shared struct {
	sync.Mutex
	dir        string
	renderer   *Renderer
	photos     []string
	album      Media
	generating Media
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared.dir != "" {
		_ = os.RemoveAll(shared.dir)
	}
	os.Exit(code)
}

func sharedDir(t testing.TB) string {
	t.Helper()
	if shared.dir == "" {
		dir, err := os.MkdirTemp("", "framy-anim-")
		if err != nil {
			t.Fatal(err)
		}
		shared.dir = dir
	}
	return shared.dir
}

// needFFmpeg returns a renderer whose ffmpeg can encode the loops, or skips
// the test: unless FRAMY_MEDIA_TESTS asks for the ffmpeg tests, as CI does in
// framy's own image, where a skip is a failure.
func needFFmpeg(t *testing.T) *Renderer {
	t.Helper()
	shared.Lock()
	defer shared.Unlock()
	if shared.renderer == nil {
		shared.renderer = &Renderer{Cache: filepath.Join(sharedDir(t), "cache")}
	}
	err := shared.renderer.Check(context.Background())
	if err == nil {
		_, err = exec.LookPath("ffprobe")
	}
	if err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot run the animation tests here: %v", err)
	}
	return shared.renderer
}

// sharedAlbum is the preview of ten photos, drawn once.
func sharedAlbum(t *testing.T) Media {
	t.Helper()
	r := needFFmpeg(t)
	shared.Lock()
	defer shared.Unlock()
	if shared.album.Path == "" {
		// Smaller photos than a phone's: the decoding is timed elsewhere.
		dir := filepath.Join(sharedDir(t), "photos")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		shared.photos = album(t, dir, 10, 3)
		m, err := r.Album(context.Background(), sharedDir(t), shared.photos)
		if err != nil {
			t.Fatal(err)
		}
		shared.album = m
	}
	return shared.album
}

// sharedGenerating is the generating loop, drawn once.
func sharedGenerating(t *testing.T) Media {
	t.Helper()
	r := needFFmpeg(t)
	shared.Lock()
	defer shared.Unlock()
	if shared.generating.Path == "" {
		m, err := r.Generating(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		shared.generating = m
	}
	return shared.generating
}

// probed is what ffprobe says of a file.
type probed struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		PixFmt     string `json:"pix_fmt"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		RFrameRate string `json:"r_frame_rate"`
		NbFrames   string `json:"nb_frames"`
		Duration   string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

func probe(t *testing.T, path string) probed {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var p probed
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// decodeFrames returns every frame of the video as raw RGB.
func decodeFrames(t *testing.T, path string, w, h int) [][]byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg decode %s: %v", path, err)
	}
	size := 3 * w * h
	if len(out)%size != 0 {
		t.Fatalf("%d bytes are not whole frames of %d×%d", len(out), w, h)
	}
	var frames [][]byte
	for i := 0; i < len(out); i += size {
		frames = append(frames, out[i:i+size])
	}
	return frames
}

// meanDiff is the mean absolute difference of two frames, per byte, 0 to 255.
func meanDiff(a, b []byte) float64 {
	var sum float64
	for i := range a {
		sum += math.Abs(float64(a[i]) - float64(b[i]))
	}
	return sum / float64(len(a))
}

// steps is the change from each frame of a loop to the next as meanDiff
// measures it, n values for the n frames; the last is the step from the final
// frame to the frame at Duration, which is the first again. Pieces of the loop
// are drawn at once: there are hundreds of frames and the race detector is
// slow.
func steps(s Scene) []float64 {
	n := Frames(s)
	w, h := s.Size()
	out := make([]float64, n)
	workers := min(runtime.GOMAXPROCS(0), 8)
	var wg sync.WaitGroup
	for k := range workers {
		from, to := k*n/workers, (k+1)*n/workers
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewCanvas(w, h)
			prev, cur := make([]byte, 3*w*h), make([]byte, 3*w*h)
			s.Draw(c, float64(from)/FPS)
			c.RGB24(prev)
			for i := from + 1; i <= to; i++ {
				s.Draw(c, float64(i)/FPS)
				c.RGB24(cur)
				out[i-1] = meanDiff(prev, cur)
				prev, cur = cur, prev
			}
		}()
	}
	wg.Wait()
	return out
}

// topBoxes lists the top-level boxes of an MP4 in order.
func topBoxes(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var boxes []string
	for {
		var head [8]byte
		if _, err := io.ReadFull(f, head[:]); err != nil {
			return boxes
		}
		size := int64(binary.BigEndian.Uint32(head[:4]))
		boxes = append(boxes, string(head[4:]))
		skip := size - 8
		if size == 1 {
			var big [8]byte
			if _, err := io.ReadFull(f, big[:]); err != nil {
				return boxes
			}
			skip = int64(binary.BigEndian.Uint64(big[:])) - 16
		}
		if size == 0 {
			return boxes
		}
		if _, err := f.Seek(skip, io.SeekCurrent); err != nil {
			return boxes
		}
	}
}

// rgb returns the pixel at x, y of a frame drawn on a canvas, 0 to 255.
func rgb(c *Canvas, x, y int) [3]int {
	i := 3 * (y*c.W + x)
	return [3]int{int(level(c.Pix[i])), int(level(c.Pix[i+1])), int(level(c.Pix[i+2]))}
}

func near(a, b [3]int, tol int) bool {
	for i := range a {
		if a[i]-b[i] > tol || b[i]-a[i] > tol {
			return false
		}
	}
	return true
}

func rgbOf(c RGB) [3]int { return [3]int{int(level(c.R)), int(level(c.G)), int(level(c.B))} }

// frameAt paints the scene at t.
func frameAt(s Scene, t float64) *Canvas {
	w, h := s.Size()
	c := NewCanvas(w, h)
	s.Draw(c, t)
	return c
}

func itoa(n int) string { return strconv.Itoa(n) }
