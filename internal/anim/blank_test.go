// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"bytes"
	"context"
	"image"
	"image/draw"
	"image/png"
	"os"
	"testing"
)

// The preview of a run with no photo is the generating loop's first frame,
// so Generate sets the same dots moving; it is drawn once.
func TestBlankIsTheLoopAtRest(t *testing.T) {
	r := &Renderer{Cache: t.TempDir()}
	m, err := r.Blank(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentType != "image/png" || m.Width != Width || m.Height != Height || m.Duration != 0 || m.Key == "" || m.Bytes > MaxCardBytes {
		t.Fatalf("%+v", m)
	}
	f, err := os.Open(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := image.NewRGBA(img.Bounds())
	draw.Draw(got, got.Bounds(), img, image.Point{}, draw.Src)
	if !bytes.Equal(got.Pix, Still(Generating{}, 0).Pix) {
		t.Error("the blank preview is not the generating loop's first frame")
	}
	again, err := r.Blank(context.Background())
	st1, _ := os.Stat(m.Path)
	st2, _ := os.Stat(again.Path)
	if err != nil || again.Path != m.Path || !os.SameFile(st1, st2) {
		t.Errorf("drawn twice: %v %v", err, again.Path)
	}
}
