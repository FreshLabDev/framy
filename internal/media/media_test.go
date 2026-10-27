// SPDX-License-Identifier: Apache-2.0
package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures in testdata are made by make.swift with Apple's ImageIO, as
// an iPhone makes them. Each is two halves of a colour, so the test can tell
// the orientation and the colours apart.

func TestSniff(t *testing.T) {
	ftyp := func(major string, compat ...string) []byte {
		b := make([]byte, 16+4*len(compat))
		binary.BigEndian.PutUint32(b, uint32(len(b)))
		copy(b[4:], "ftyp"+major+"\x00\x00\x00\x00")
		for i, c := range compat {
			copy(b[16+4*i:], c)
		}
		return b
	}
	for _, c := range []struct {
		name string
		head []byte
		want Format
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, JPEG},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00"), PNG},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), WebP},
		{"iphone heic", ftyp("heic", "mif1", "MiHE", "miaf", "MiHB", "heic"), HEIF},
		{"mif1 first", ftyp("mif1", "heic"), HEIF},
		{"avif", ftyp("avif", "mif1", "miaf"), AVIF},
		// An AVIF names mif1 first sometimes; avif still wins.
		{"avif after mif1", ftyp("mif1", "avif", "miaf"), AVIF},
		{"animated avif", ftyp("avis", "msf1"), AVIF},
		{"mp4 video", ftyp("isom", "iso2", "avc1", "mp41"), ""},
		{"quicktime", ftyp("qt  ", "qt  "), ""},
		{"gif", []byte("GIF89a\x01\x00"), ""},
		{"text", []byte("hello world, not a picture"), ""},
		{"empty", nil, ""},
	} {
		if got := Sniff(c.head); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func readFixture(t *testing.T, name string) header {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := readFile(f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return h
}

func TestHeaders(t *testing.T) {
	for _, c := range []struct {
		file string
		f    Format
		w, h int
		p3   bool
	}{
		// The grid's canvas is the largest item; the tiles are 512.
		{"iphone.heic", HEIF, 1600, 1200, true},
		{"tiled.avif", AVIF, 1600, 1200, false},
		{"small.avif", AVIF, 320, 240, false},
		{"exif.jpg", JPEG, 400, 300, false},
		{"p3.jpg", JPEG, 400, 300, true},
	} {
		h := readFixture(t, c.file)
		if h.format != c.f || h.width != c.w || h.height != c.h || h.p3 != c.p3 {
			t.Errorf("%s: %s %d×%d p3=%v, want %s %d×%d p3=%v", c.file, h.format, h.width, h.height, h.p3, c.f, c.w, c.h, c.p3)
		}
	}
}

func TestWebPSizes(t *testing.T) {
	riff := func(chunk string, data []byte) []byte {
		b := append([]byte("RIFF\x00\x00\x00\x00WEBP"+chunk), 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(b[16:], uint32(len(data)))
		return append(b, data...)
	}
	vp8x := make([]byte, 10)
	vp8x[4], vp8x[5], vp8x[6] = 0x3F, 0x1F, 0x00 // 8000 - 1
	vp8x[7], vp8x[8], vp8x[9] = 0x9F, 0x0F, 0x00 // 4000 - 1
	vp8l := []byte{0x2F, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(vp8l[1:], uint32(640-1)|uint32(480-1)<<14)
	vp8 := []byte{0, 0, 0, 0x9D, 0x01, 0x2A, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(vp8[6:], 1024)
	binary.LittleEndian.PutUint16(vp8[8:], 768)
	for _, c := range []struct {
		name string
		b    []byte
		w, h int
	}{
		{"extended", riff("VP8X", vp8x), 8000, 4000},
		{"lossless", riff("VP8L", vp8l), 640, 480},
		{"lossy", riff("VP8 ", vp8), 1024, 768},
	} {
		if w, h := webpSize(c.b); w != c.w || h != c.h {
			t.Errorf("%s: %d×%d, want %d×%d", c.name, w, h, c.w, c.h)
		}
	}
}

// A profile that is not Display P3 is not converted: its pixels stay as
// they are, with the profile.
func TestOnlyDisplayP3IsConverted(t *testing.T) {
	icc := func(colorants [3][3]float64) []byte {
		b := make([]byte, 132+3*12+3*20)
		copy(b[16:], "RGB ")
		copy(b[36:], "acsp")
		binary.BigEndian.PutUint32(b[128:], 3)
		for i, sig := range []string{"rXYZ", "gXYZ", "bXYZ"} {
			off := 132 + 36 + 20*i
			copy(b[132+12*i:], sig)
			binary.BigEndian.PutUint32(b[132+12*i+4:], uint32(off))
			binary.BigEndian.PutUint32(b[132+12*i+8:], 20)
			copy(b[off:], "XYZ ")
			for k, v := range colorants[i] {
				binary.BigEndian.PutUint32(b[off+8+4*k:], uint32(int32(v*65536)))
			}
		}
		return b
	}
	srgb := [3][3]float64{{0.4361, 0.2225, 0.0139}, {0.3851, 0.7169, 0.0971}, {0.1431, 0.0606, 0.7141}}
	adobe := [3][3]float64{{0.6097, 0.3111, 0.0195}, {0.2053, 0.6257, 0.0609}, {0.1492, 0.0632, 0.7446}}
	if !iccIsP3(icc(displayP3)) {
		t.Error("Display P3 is not recognised")
	}
	if iccIsP3(icc(srgb)) || iccIsP3(icc(adobe)) {
		t.Error("sRGB or Adobe RGB taken for Display P3")
	}
	cut := icc(displayP3)
	if iccIsP3(cut[:200]) || iccIsP3(nil) {
		t.Error("a cut profile taken for Display P3")
	}
}

// converter skips the ffmpeg tests where ffmpeg cannot run them, unless
// FRAMY_MEDIA_TESTS asks for them, as CI does in framy's own image.
func converter(t *testing.T) *Converter {
	t.Helper()
	c := &Converter{}
	if err := c.Check(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot run the media tests here: %v", err)
	}
	return c
}

func normalize(t *testing.T, c *Converter, data []byte) (Image, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, In), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return c.Normalize(context.Background(), dir)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decode(t *testing.T, img Image) (image.Image, []byte) {
	t.Helper()
	raw, err := os.ReadFile(img.Path)
	if err != nil {
		t.Fatal(err)
	}
	var pic image.Image
	switch img.ContentType {
	case "image/jpeg":
		pic, err = jpeg.Decode(bytes.NewReader(raw))
	case "image/png":
		pic, err = png.Decode(bytes.NewReader(raw))
	default:
		t.Fatalf("content type %q", img.ContentType)
	}
	if err != nil {
		t.Fatal(err)
	}
	if b := pic.Bounds(); b.Dx() != img.Width || b.Dy() != img.Height {
		t.Fatalf("the image says %d×%d, the picture is %v", img.Width, img.Height, b)
	}
	return pic, raw
}

func hex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("%02x%02x%02x", r>>8, g>>8, b>>8)
}

// near: JPEG moves a flat colour by a few units.
func near(c color.Color, want string) bool {
	var wr, wg, wb int
	if _, err := fmt.Sscanf(want, "%02x%02x%02x", &wr, &wg, &wb); err != nil {
		panic(err)
	}
	r, g, b, _ := c.RGBA()
	d := func(a uint32, b int) int { return max(int(a>>8)-b, b-int(a>>8)) }
	return d(r, wr) <= 6 && d(g, wg) <= 6 && d(b, wb) <= 6
}

// Display P3 (0.60, 0.45, 0.30) and (0.25, 0.40, 0.65) in sRGB, as macOS
// ColorSync converts them.
const (
	p3Warm = "a07148"
	p3Cool = "3367aa"
	red    = "cc3333"
	blue   = "334dcc"
)

func TestNormalizeFixtures(t *testing.T) {
	c := converter(t)
	for _, tc := range []struct {
		file string
		w, h int
		// first and second are the colours a quarter in from the top and
		// the bottom (tall) or the left and the right (wide).
		first, second string
	}{
		// Tiles joined, turned a quarter clockwise, P3 into sRGB.
		{"iphone.heic", 1200, 1600, p3Warm, p3Cool},
		// Tiles joined. ImageIO keeps the AVIF's orientation in EXIF only,
		// which a HEIF reader ignores: ffmpeg and macOS show it as stored.
		{"tiled.avif", 1600, 1200, red, blue},
		{"small.avif", 320, 240, red, blue},
		// Turned a quarter the other way by EXIF.
		{"exif.jpg", 300, 400, blue, red},
		{"p3.jpg", 400, 300, p3Warm, p3Cool},
	} {
		t.Run(tc.file, func(t *testing.T) {
			img, err := normalize(t, c, fixture(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if img.ContentType != "image/jpeg" || img.Width != tc.w || img.Height != tc.h {
				t.Fatalf("%s %d×%d, want image/jpeg %d×%d", img.ContentType, img.Width, img.Height, tc.w, tc.h)
			}
			pic, raw := decode(t, img)
			var a, b color.Color
			if tc.h > tc.w {
				a, b = pic.At(tc.w/2, tc.h/4), pic.At(tc.w/2, tc.h*3/4)
			} else {
				a, b = pic.At(tc.w/4, tc.h/2), pic.At(tc.w*3/4, tc.h/2)
			}
			if !near(a, tc.first) || !near(b, tc.second) {
				t.Errorf("colours %s and %s, want %s and %s", hex(a), hex(b), tc.first, tc.second)
			}
			assertNoMetadata(t, raw, tc.first == p3Warm)
		})
	}
}

// assertNoMetadata checks the JPEG carries no EXIF, no GPS and, once the
// colours are sRGB, no profile that would convert them again.
func assertNoMetadata(t *testing.T, raw []byte, converted bool) {
	t.Helper()
	jpegSegments(raw, func(marker byte, seg []byte) {
		if marker == 0xE1 {
			t.Errorf("an APP1 segment is left: %q", seg[:min(len(seg), 8)])
		}
	})
	if bytes.Contains(raw, []byte("GPS")) || bytes.Contains(raw, []byte("iPhone")) {
		t.Error("the GPS or the camera's EXIF is left")
	}
	if converted && jpegICC(raw) != nil {
		t.Error("the P3 profile is left on sRGB pixels")
	}
}

func TestAlphaStaysPNG(t *testing.T) {
	c := converter(t)
	pic := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for y := range 200 {
		for x := range 300 {
			if x < 150 {
				pic.SetNRGBA(x, y, color.NRGBA{R: 200, G: 40, B: 40, A: 128})
			} else {
				pic.SetNRGBA(x, y, color.NRGBA{R: 40, G: 60, B: 200, A: 0})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pic); err != nil {
		t.Fatal(err)
	}
	img, err := normalize(t, c, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if img.ContentType != "image/png" || img.Width != 300 || img.Height != 200 || filepath.Ext(img.Path) != ".png" {
		t.Fatalf("%s %d×%d %s, want image/png 300×200", img.ContentType, img.Width, img.Height, img.Path)
	}
	out, _ := decode(t, img)
	left := color.NRGBAModel.Convert(out.At(75, 100)).(color.NRGBA)
	right := color.NRGBAModel.Convert(out.At(225, 100)).(color.NRGBA)
	if left.A < 120 || left.A > 136 || left.R < 190 || right.A != 0 {
		t.Errorf("alpha lost: left %v, right %v", left, right)
	}
}

func TestTheLongSideComesDown(t *testing.T) {
	c := converter(t)
	pic := image.NewRGBA(image.Rect(0, 0, 3000, 1000))
	for y := range 1000 {
		for x := range 3000 {
			pic.Set(x, y, color.RGBA{R: uint8(x / 12), G: uint8(y / 4), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pic); err != nil {
		t.Fatal(err)
	}
	img, err := normalize(t, c, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// An opaque PNG becomes a JPEG.
	if img.ContentType != "image/jpeg" || img.Width != 2048 || img.Height < 682 || img.Height > 683 {
		t.Errorf("%s %d×%d, want image/jpeg 2048×683", img.ContentType, img.Width, img.Height)
	}
	// A picture within the long side keeps its size.
	small, err := normalize(t, c, fixture(t, "exif.jpg"))
	if err != nil || small.Width != 300 {
		t.Errorf("%+v %v", small, err)
	}
}

func TestRefusals(t *testing.T) {
	c := &Converter{FFmpeg: "/nonexistent/ffmpeg", FFprobe: "/nonexistent/ffprobe"}
	// A PNG that declares 10000×10000 is refused from its header, before
	// any ffmpeg runs: these paths would fail otherwise.
	bomb := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	bomb = binary.BigEndian.AppendUint32(bomb, 10000)
	bomb = binary.BigEndian.AppendUint32(bomb, 10000)
	bomb = append(bomb, 8, 6, 0, 0, 0, 0, 0, 0, 0)
	if _, err := normalize(t, c, bomb); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a 100 MP PNG: %v, want ErrTooLarge", err)
	}
	sof := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08}
	sof = binary.BigEndian.AppendUint16(sof, 9000)
	sof = binary.BigEndian.AppendUint16(sof, 9000)
	sof = append(sof, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1)
	if _, err := normalize(t, c, sof); !errors.Is(err, ErrTooLarge) {
		t.Errorf("an 81 MP JPEG: %v, want ErrTooLarge", err)
	}
	if _, err := normalize(t, c, []byte("GIF89a\x01\x00\x01\x00")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a GIF: %v, want ErrUnsupported", err)
	}
	if _, err := normalize(t, c, []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an SVG: %v, want ErrUnsupported", err)
	}
	// An ftyp of a still image without its meta box.
	heic := fixture(t, "iphone.heic")
	if _, err := normalize(t, c, heic[:40]); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a cut HEIC: %v, want ErrUnreadable", err)
	}
	// The HEIC's own grid is 1600×1200: with a smaller cap it is refused
	// from its header too.
	small := &Converter{FFmpeg: c.FFmpeg, FFprobe: c.FFprobe, MaxPixels: 1_000_000}
	if _, err := normalize(t, small, heic); !errors.Is(err, ErrTooLarge) {
		t.Errorf("the HEIC over a 1 MP cap: %v, want ErrTooLarge", err)
	}
}

// A cut JPEG is shown as far as it goes, as a browser shows it; a cut
// upload is refused before, by the length of the body.
func TestBrokenPicturesAreUnreadable(t *testing.T) {
	c := converter(t)
	if _, err := normalize(t, c, []byte("\x89PNG\r\n\x1a\nnot really")); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a broken PNG: %v, want ErrUnreadable", err)
	}
	webp := []byte("RIFF\x10\x00\x00\x00WEBPVP8 \x04\x00\x00\x00\x00\x00\x00\x00")
	if _, err := normalize(t, c, webp); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a broken WebP: %v, want ErrUnreadable", err)
	}
}

func TestATimeoutStopsFFmpeg(t *testing.T) {
	c := converter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, In), fixture(t, "exif.jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := c.Normalize(ctx, dir)
	if err == nil || errors.Is(err, ErrUnreadable) {
		t.Errorf("a stopped context: %v, want the context's error, not ErrUnreadable", err)
	}
}

func TestWorkDirs(t *testing.T) {
	w := Work{Root: filepath.Join(t.TempDir(), "work")}
	dir, done, err := w.Dir("upload")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), "upload-") {
		t.Errorf("dir %s", dir)
	}
	if err := os.WriteFile(filepath.Join(dir, In), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	done()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory outlived its work: %v", err)
	}

	old, _, _ := w.Dir("crashed")
	fresh, _, _ := w.Dir("running")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	n, err := w.Sweep(time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a crash's leftovers stayed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("the sweep took a running job's directory")
	}
	if n, err := (Work{Root: filepath.Join(t.TempDir(), "none")}).Sweep(time.Hour); n != 0 || err != nil {
		t.Errorf("a missing root: %d %v", n, err)
	}
}
