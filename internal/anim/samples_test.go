// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSamples writes the pictures to look at: set FRAMY_ANIM_SAMPLES to a
// directory. FRAMY_ANIM_PHOTOS may name a directory of JPEGs to use instead of
// the made-up landscapes. It prints what each took.
func TestSamples(t *testing.T) {
	out := os.Getenv("FRAMY_ANIM_SAMPLES")
	if out == "" {
		t.Skip("set FRAMY_ANIM_SAMPLES to a directory to write the sample pictures")
	}
	r := &Renderer{Cache: filepath.Join(out, "cache")}
	if err := r.Check(context.Background()); err != nil {
		t.Skip(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "extras"), 0o755); err != nil {
		t.Fatal(err)
	}
	photos := func(n int) []string {
		if dir := os.Getenv("FRAMY_ANIM_PHOTOS"); dir != "" {
			all, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
			return all[:min(n, len(all))]
		}
		return album(t, t.TempDir(), n, 1)
	}
	still := func(name string, s Scene, at float64) {
		writePNG(t, filepath.Join(out, name), Still(s, at))
	}
	for _, c := range []struct {
		name string
		n    int
		dir  string
	}{{"album", 5, ""}, {"album-1", 1, "extras"}, {"album-3", 3, "extras"}, {"album-10", 10, "extras"}} {
		paths := photos(c.n)
		start := time.Now()
		m, err := r.Album(context.Background(), t.TempDir(), paths)
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		dst := filepath.Join(out, c.dir, c.name+".mp4")
		if err := os.Rename(m.Path, dst); err != nil {
			t.Fatal(err)
		}
		a, _ := NewAlbum(context.Background(), paths)
		still(filepath.Join(c.dir, c.name+"-still.png"), a, 0)
		if c.name == "album" {
			still(filepath.Join("extras", "album-flying.png"), a, a.enters(0)+0.5)
			still(filepath.Join("extras", "album-empty.png"), a, a.enters(0)-0.2)
		}
		t.Logf("%s: %d photos, %s, %d KB, rendered in %s", c.name, c.n, m.Duration, m.Bytes/1024, took.Round(time.Millisecond))
	}
	start := time.Now()
	m, err := r.Generating(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("generating: %s, %d KB, rendered in %s", m.Duration, m.Bytes/1024, time.Since(start).Round(time.Millisecond))
	if err := os.Rename(m.Path, filepath.Join(out, "generating.mp4")); err != nil {
		t.Fatal(err)
	}
	still("generating-still.png", Generating{}, 0)
	still(filepath.Join("extras", "generating-1.5s.png"), Generating{}, 1.5)

	for _, c := range []struct{ name, text, lang string }{
		{"error-card", "The model refused this picture. Try other words.", "en"},
		{"error-ru", "Модель отказалась: в запросе нашлось то, что запрещено правилами. Попробуйте другие слова.", "ru"},
		{"error-de", "Das Guthaben des Anbieters reicht nicht aus. Bitte versuche es später noch einmal.", "de"},
		{"error-uk", "Модель відмовилась від цієї картинки. Спробуйте інші слова.", "uk"},
		{"error-pl", "Dostawca nie zaakceptował klucza. Sprawdź go w ustawieniach.", "pl"},
		{"error-ja", "モデルがこの画像を拒否しました。", "ja"},
		{"error-long", "Przekroczenie limitu: ten dostawca przyjmuje na raz najwyżej dziesięć zdjęć, a w albumie było ich dużo więcej niż to możliwe, więc spróbuj wybrać mniej zdjęć i uruchomić jeszcze raz", "pl"},
	} {
		start := time.Now()
		m, err := r.ErrorCard(context.Background(), c.text, c.lang)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(out, "extras", c.name+".png")
		if c.name == "error-card" {
			dst = filepath.Join(out, "error-card.png")
		}
		if err := os.Rename(m.Path, dst); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d KB, text drawn %v, rendered in %s", c.name, m.Bytes/1024, m.TextDrawn, time.Since(start).Round(time.Millisecond))
	}
}
