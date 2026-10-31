// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreviewSamples writes preview cards to look at: set
// FRAMY_ANIM_SAMPLES to a directory.
func TestPreviewSamples(t *testing.T) {
	out := os.Getenv("FRAMY_ANIM_SAMPLES")
	if out == "" {
		t.Skip("set FRAMY_ANIM_SAMPLES to a directory to write the sample pictures")
	}
	dir := filepath.Join(out, "preview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	photos := album(t, t.TempDir(), 3, 1)
	for _, c := range []struct {
		name  string
		p     Preview
		paths []string
	}{
		{"free-none-ru", Preview{Lang: "ru", Maker: "Microsoft AI", Model: "MAI-Image-2.6 Flash", Chips: []string{"1K", "×1"}, Free: true, Left: 4, Limit: 5, Label: "бесплатно"}, nil},
		{"free-3-uk", Preview{Lang: "uk", Maker: "Microsoft AI", Model: "MAI-Image-2.6 Flash", Chips: []string{"1K", "×1"}, Free: true, Left: 5, Limit: 5, Label: "безкоштовно"}, photos},
		{"key-169-en", Preview{Lang: "en", Maker: "Google", Model: "Nano Banana Pro", Aspect: "16:9", Chips: []string{"2K", "×1"}, Price: "$0.134", Local: "≈ 5,63 ₴", LocalCode: "≈ 5,63 UAH", Label: "your OpenRouter key"}, nil},
		{"key-916-ru", Preview{Lang: "ru", Maker: "Black Forest Labs", Model: "FLUX 3 Image Klein", Aspect: "9:16", Chips: []string{"1.5K", "×1"}, Price: "$0.021", Local: "≈ 2,05 ₽", LocalCode: "≈ 2,05 RUB", Label: "твой ключ OpenRouter"}, nil},
		{"key-1-ja", Preview{Lang: "ja", Maker: "OpenAI", Model: "gpt-image-2.5-sunburst", Aspect: "3:2", Chips: []string{"high", "×1"}, Price: "$0.25", Label: "OpenRouter のキー"}, photos[:1]},
	} {
		p, err := NewPreview(context.Background(), c.p, c.paths)
		if err != nil {
			t.Fatal(err)
		}
		writePNG(t, filepath.Join(dir, c.name+".png"), Still(p, 0))
	}
}

func TestPreviewIsAPNGWithoutPhotosAndAJPEGWithThem(t *testing.T) {
	r := &Renderer{Cache: t.TempDir()}
	dir := t.TempDir()
	p := Preview{Lang: "en", Maker: "Google", Model: "Nano Banana Pro", Aspect: "16:9", Price: "$0.134", Label: "your OpenRouter key"}
	m, err := r.Preview(context.Background(), dir, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentType != "image/png" || m.Width != Width || m.Height != Height || !m.TextDrawn || m.Bytes == 0 || m.Bytes > MaxCardBytes {
		t.Fatalf("card = %+v", m)
	}
	photos := album(t, t.TempDir(), 3, 1)
	m, err = r.Preview(context.Background(), dir, p, photos)
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentType != "image/jpeg" || filepath.Ext(m.Path) != ".jpg" || m.Bytes == 0 || m.Bytes > MaxCardBytes {
		t.Fatalf("card with photos = %+v", m)
	}
}

func TestPreviewLeavesOutWhatTheFacesCannotSpell(t *testing.T) {
	p, err := NewPreview(context.Background(), Preview{
		Lang: "ja", Model: "gpt-image-2", Label: "OpenRouter のキー", Price: "価格不明", Local: "≈ 5,63 ₴", LocalCode: "≈ 5,63 UAH",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Label != "" || p.Price != "" || p.Local != "" {
		t.Fatalf("drawn: %+v", p)
	}
	p, _ = NewPreview(context.Background(), Preview{Lang: "uk", Model: "x", Price: "$1", Local: "≈ 5,63 ₴", LocalCode: "≈ 5,63 UAH"}, nil)
	if p.Local != "≈ 5,63 UAH" {
		t.Fatalf("local = %q: the faces have no ₴", p.Local)
	}
}

func TestPreviewTakesAtMostTenPhotos(t *testing.T) {
	if _, err := NewPreview(context.Background(), Preview{Model: "x"}, make([]string, MaxPhotos+1)); err != ErrNoPhotos {
		t.Fatalf("eleven photos: %v", err)
	}
}

func TestALongNameFitsTheColumn(t *testing.T) {
	p, err := NewPreview(context.Background(), Preview{Lang: "en", Model: "Nano Banana Pro (Gemini 3 Pro Image Preview) Ultra Long Edition"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Canvas{Lang: p.lang}
	if len(p.name) != 2 || !strings.HasSuffix(p.name[1], "…") {
		t.Errorf("name = %q at %.0f", p.name, p.size)
	}
	for _, l := range p.name {
		if w := c.Measure(Display, p.size, l); w > pvColW {
			t.Errorf("%q is %.0f wide, the column %d", l, w, pvColW)
		}
	}
}
