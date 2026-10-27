// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultCRF is the x264 quality: lower is better. 22 keeps the edges of
	// a photo and the figures on it clean at a few hundred kilobytes.
	DefaultCRF = 22
	// DefaultPreset is the x264 preset.
	DefaultPreset = "medium"
	// DefaultTimeout bounds one encoding, frames and ffmpeg together.
	DefaultTimeout = time.Minute
	// maxWorkers caps the frames drawn at once: a canvas holds about 8 MB.
	maxWorkers = 8
	// stderrTail is how much of ffmpeg's complaint goes into an error.
	stderrTail = 1024
)

// Options tune the encoding. The zero value is what framy uses.
type Options struct {
	Workers int           // frames drawn at once; 0 is one per CPU, at most 8
	CRF     int           // x264 quality; 0 is DefaultCRF
	Preset  string        // x264 preset; "" is DefaultPreset
	FFmpeg  string        // the ffmpeg binary; "" is ffmpeg on PATH
	Timeout time.Duration // 0 is DefaultTimeout
}

func (o Options) withDefaults() Options {
	if o.Workers <= 0 {
		o.Workers = min(runtime.GOMAXPROCS(0), maxWorkers)
	}
	if o.CRF == 0 {
		o.CRF = DefaultCRF
	}
	if o.Preset == "" {
		o.Preset = DefaultPreset
	}
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	return o
}

// Stats is what an encoding took.
type Stats struct {
	Frames int
	Draw   time.Duration // summed over the workers
	Total  time.Duration
}

// Encode draws every frame of one loop of the scene and pipes them to ffmpeg,
// which writes an H.264 MP4 without sound to out: what Telegram plays as a
// GIF. Frames are drawn in parallel and written in order. The file appears
// under its name only when it is whole.
//
// ffmpeg gets an empty environment and reads nothing but the pipe.
func Encode(ctx context.Context, s Scene, out string, o Options) (Stats, error) {
	start := time.Now()
	o = o.withDefaults()
	w, h := s.Size()
	if w%2 != 0 || h%2 != 0 || w <= 0 || h <= 0 || Frames(s) < 1 {
		return Stats{}, fmt.Errorf("anim: a loop of %d×%d and %d frames cannot be encoded as yuv420p", w, h, Frames(s))
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	part := out + ".part"
	defer os.Remove(part)
	cmd := exec.CommandContext(ctx, o.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-protocol_whitelist", "pipe",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-s", fmt.Sprintf("%dx%d", w, h), "-r", strconv.Itoa(FPS), "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", o.Preset, "-crf", strconv.Itoa(o.CRF),
		"-vf", "scale=out_color_matrix=bt709:out_range=tv", "-pix_fmt", "yuv420p",
		"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=tv",
		"-movflags", "+faststart", "-f", "mp4", part)
	cmd.Env = []string{}
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Stats{}, err
	}
	if err := cmd.Start(); err != nil {
		return Stats{}, fmt.Errorf("start ffmpeg: %w", err)
	}
	st, werr := frames(ctx, s, o.Workers, stdin)
	stdin.Close()
	cerr := cmd.Wait()
	// Whoever stopped it, the caller or the timeout, is the reason; else
	// ffmpeg's own words, which also explain a write that found the pipe
	// closed; else the write.
	switch {
	case ctx.Err() != nil:
		return st, fmt.Errorf("ffmpeg: %w", ctx.Err())
	case cerr != nil:
		return st, fmt.Errorf("ffmpeg: %w: %s", cerr, tail(stderr.String()))
	case werr != nil:
		return st, werr
	}
	if err := os.Rename(part, out); err != nil {
		return st, err
	}
	st.Total = time.Since(start)
	return st, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > stderrTail {
		return "…" + s[len(s)-stderrTail:]
	}
	return s
}

// frames draws the loop with workers goroutines and writes the frames to w as
// raw RGB in order. At most two frames per worker wait to be written.
func frames(ctx context.Context, s Scene, workers int, w io.Writer) (Stats, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	n := Frames(s)
	width, height := s.Size()
	size := 3 * width * height
	type frame struct {
		i   int
		buf []byte
	}
	pool := sync.Pool{New: func() any { return make([]byte, size) }}
	jobs := make(chan int)
	done := make(chan frame, 2*workers)
	slots := make(chan struct{}, 2*workers)
	var drawn time.Duration
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewCanvas(width, height)
			for i := range jobs {
				t0 := time.Now()
				s.Draw(c, float64(i)/FPS)
				buf := pool.Get().([]byte)
				c.RGB24(buf)
				mu.Lock()
				drawn += time.Since(t0)
				mu.Unlock()
				done <- frame{i, buf}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range n {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(done) }()

	waiting := map[int][]byte{}
	next := 0
	var err error
	for f := range done {
		waiting[f.i] = f.buf
		for buf, ok := waiting[next]; ok; buf, ok = waiting[next] {
			delete(waiting, next)
			if err == nil {
				if _, werr := w.Write(buf); werr != nil {
					err = fmt.Errorf("write frame %d: %w", next, werr)
					cancel()
				}
			}
			pool.Put(buf)
			<-slots
			next++
		}
	}
	if err == nil && next != n {
		err = errors.Join(ctx.Err(), fmt.Errorf("wrote %d of %d frames", next, n))
	}
	return Stats{Frames: next, Draw: drawn}, err
}

// Still draws the frame of the scene at t seconds.
func Still(s Scene, t float64) *image.RGBA {
	w, h := s.Size()
	c := NewCanvas(w, h)
	s.Draw(c, t)
	return c.Image()
}
