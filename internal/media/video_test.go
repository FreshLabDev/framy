// SPDX-License-Identifier: Apache-2.0
package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSniffVideo(t *testing.T) {
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
		want string
	}{
		{"mp4", ftyp("isom", "iso2", "avc1", "mp41"), "mov"},
		{"quicktime", ftyp("qt  ", "qt  "), "mov"},
		{"3gp", ftyp("3gp4", "isom"), "mov"},
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86}, "matroska"},
		{"heic", ftyp("heic", "mif1", "heic"), ""},
		{"avif", ftyp("avif", "mif1", "miaf"), ""},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, ""},
		{"text", []byte("hello world, not a clip"), ""},
		{"empty", nil, ""},
	} {
		if got := SniffVideo(c.head); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestACutIsValid(t *testing.T) {
	for _, c := range []struct {
		cut  Cut
		want bool
	}{
		{Cut{0, 5}, true},
		{Cut{2.5, 0.1}, true},
		{Cut{-1, 5}, false},
		{Cut{0, 0.05}, false},
		{Cut{math.NaN(), 5}, false},
		{Cut{0, math.Inf(1)}, false},
	} {
		if got := c.cut.Valid(); got != c.want {
			t.Errorf("%+v: %v", c.cut, got)
		}
	}
}

func TestAFlatFrameIsTold(t *testing.T) {
	plain := image.NewRGBA(image.Rect(0, 0, 64, 36))
	if !flat(plain) {
		t.Error("a black frame is not flat")
	}
	for x := range 32 {
		for y := range 36 {
			plain.Pix[(y*64+x)*4] = 255
			plain.Pix[(y*64+x)*4+3] = 255
		}
	}
	if flat(plain) {
		t.Error("half red, half black is flat")
	}
}

// videoConverter skips where ffmpeg cannot make clips, unless
// FRAMY_MEDIA_TESTS asks for the tests.
func videoConverter(t *testing.T) *Converter {
	t.Helper()
	c := converter(t)
	if err := c.CheckVideo(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot make clips here: %v", err)
	}
	return c
}

// makeClip writes a clip made by ffmpeg with args as dir/In and returns dir.
func makeClip(t *testing.T, args ...string) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, In)
	cmd := exec.Command("ffmpeg", append(append([]string{"-hide_banner", "-v", "error", "-nostdin"}, args...), "-y", out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making the clip: %v: %s", err, b)
	}
	return dir
}

// h264 is a clip of the test pattern with a tone, as a phone would make it.
func h264(t *testing.T, size string, secs string, extra ...string) string {
	t.Helper()
	args := []string{
		"-f", "lavfi", "-i", "testsrc2=s=" + size + ":r=30:d=" + secs,
		"-f", "lavfi", "-i", "sine=f=440:d=" + secs,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-preset", "ultrafast", "-c:a", "aac", "-shortest",
	}
	return makeClip(t, append(append(args, extra...), "-f", "mp4")...)
}

type streamInfo struct {
	codec, pixFmt, transfer string
	width, height           int
	audio                   string
}

// streamsOf reads what ffprobe says of a file framy wrote.
func streamsOf(t *testing.T, path string) streamInfo {
	t.Helper()
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,codec_name,pix_fmt,color_transfer,width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var s streamInfo
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(line, ",")
		switch {
		case len(f) >= 6 && f[1] == "video":
			s.codec, s.pixFmt, s.transfer = f[0], f[4], f[5]
			s.width, s.height = atoi(f[2]), atoi(f[3])
		case len(f) >= 2 && f[1] == "audio":
			s.audio = f[0]
		}
	}
	return s
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}

func TestAPhonesClipIsCopiedWithoutItsPlace(t *testing.T) {
	c := videoConverter(t)
	dir := h264(t, "640x360", "2", "-metadata", "location=+48.8584+002.2945/", "-metadata", "title=Holiday")
	v, err := c.NormalizeVideo(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Copied || v.ContentType != "video/mp4" || v.Width != 640 || v.Height != 360 || !v.Audio ||
		math.Abs(v.Seconds-2) > 0.1 || math.Abs(v.FPS-30) > 0.01 {
		t.Fatalf("%+v", v)
	}
	raw, err := os.ReadFile(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"48.8584", "Holiday"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("the copy keeps %q", s)
		}
	}
	// faststart: the index comes before the frames.
	if i, j := bytes.Index(raw, []byte("moov")), bytes.Index(raw, []byte("mdat")); i < 0 || j < 0 || i > j {
		t.Errorf("moov at %d, mdat at %d", i, j)
	}
}

func TestARotatedClipComesUpright(t *testing.T) {
	c := videoConverter(t)
	src := h264(t, "640x360", "1")
	dir := makeClip(t, "-display_rotation", "90", "-i", filepath.Join(src, In), "-c", "copy", "-f", "mp4")
	v, err := c.NormalizeVideo(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Copied || v.Width != 360 || v.Height != 640 {
		t.Fatalf("%+v", v)
	}
	if s := streamsOf(t, v.Path); s.width != 360 || s.height != 640 {
		t.Errorf("the file is %d×%d", s.width, s.height)
	}
}

func TestOtherClipsAreEncodedAgain(t *testing.T) {
	c := videoConverter(t)
	for name, args := range map[string][]string{
		// MPEG-4 Part 2 with PCM sound in Matroska: neither codec is kept.
		"mkv": {"-f", "lavfi", "-i", "testsrc2=s=321x241:r=25:d=1", "-f", "lavfi", "-i", "sine=d=1",
			"-c:v", "mpeg4", "-c:a", "pcm_s16le", "-shortest", "-f", "matroska"},
		// More than 1080p and more than 60 frames a second, and no sound.
		"big": {"-f", "lavfi", "-i", "testsrc2=s=2560x1440:r=120:d=0.5", "-c:v", "libx264", "-preset", "ultrafast",
			"-pix_fmt", "yuv420p", "-f", "mp4"},
		// A bitrate no editor takes: lossless noise.
		"noise": {"-f", "lavfi", "-i", "nullsrc=s=320x240:r=30:d=1,geq=random(1)*255:128:128", "-c:v", "libx264",
			"-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv420p", "-f", "mp4"},
	} {
		dir := makeClip(t, args...)
		v, err := c.NormalizeVideo(context.Background(), dir, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		s := streamsOf(t, v.Path)
		if v.Copied || s.codec != "h264" || s.pixFmt != "yuv420p" || v.Width%2 != 0 || v.Height%2 != 0 {
			t.Errorf("%s: %+v, %+v", name, v, s)
		}
		switch name {
		case "mkv":
			if s.audio != "aac" || !v.Audio {
				t.Errorf("mkv: sound %q", s.audio)
			}
		case "big":
			if v.Width != 1920 || v.Height != 1080 || v.FPS > 60.01 || v.Audio {
				t.Errorf("big: %+v", v)
			}
		case "noise":
			if bits := float64(v.Bytes) * 8 / v.Seconds; bits > 9_500_000 {
				t.Errorf("noise: %.0f bit/s", bits)
			}
		}
	}
}

func TestAnHDRClipComesToSDR(t *testing.T) {
	c := videoConverter(t)
	dir := t.TempDir()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=1",
		"-vf", "setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc,format=yuv420p10le",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-f", "mp4", "-y", filepath.Join(dir, In))
	if b, err := cmd.CombinedOutput(); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("a 10-bit clip: %v: %s", err, b)
		}
		t.Skipf("this ffmpeg makes no 10-bit H.264: %s", b)
	}
	v, err := c.NormalizeVideo(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := streamsOf(t, v.Path)
	if v.Copied || !v.HDR || s.pixFmt != "yuv420p" || s.transfer != "bt709" {
		t.Fatalf("%+v, %+v", v, s)
	}
	if os.Getenv("FRAMY_MEDIA_TESTS") != "" && !c.toneMaps(context.Background()) {
		t.Error("framy's image has no zscale: HDR is not tone mapped")
	}
}

func TestACutIsThePartAskedFor(t *testing.T) {
	c := videoConverter(t)
	dir := h264(t, "640x360", "4")
	v, err := c.NormalizeVideo(context.Background(), dir, &Cut{Start: 1, Seconds: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if v.Copied || math.Abs(v.Seconds-1.5) > 0.1 || !v.Audio {
		t.Fatalf("%+v", v)
	}
	// A part that runs past the end ends with the clip.
	v, err = c.NormalizeVideo(context.Background(), dir, &Cut{Start: 3, Seconds: 5})
	if err != nil || math.Abs(v.Seconds-1) > 0.1 {
		t.Fatalf("%+v, %v", v, err)
	}
	for _, cut := range []Cut{{Start: 4, Seconds: 1}, {Start: -1, Seconds: 1}, {Start: 0, Seconds: 0}, {Start: 3.95, Seconds: 1}} {
		if _, err := c.NormalizeVideo(context.Background(), dir, &cut); !errors.Is(err, ErrBadCut) {
			t.Errorf("%+v: %v", cut, err)
		}
	}
}

func TestALongClipIsCutFirst(t *testing.T) {
	c := videoConverter(t)
	c.MaxSeconds = 2
	dir := h264(t, "320x180", "3")
	_, err := c.NormalizeVideo(context.Background(), dir, nil)
	var long *TooLongError
	if !errors.As(err, &long) || math.Abs(long.Seconds-3) > 0.1 || long.Max != 2 {
		t.Fatalf("%v", err)
	}
	if _, err := c.NormalizeVideo(context.Background(), dir, &Cut{Start: 0.5, Seconds: 2}); err != nil {
		t.Fatalf("a 2 s part: %v", err)
	}
	if _, err := c.NormalizeVideo(context.Background(), dir, &Cut{Start: 0, Seconds: 2.5}); !errors.As(err, &long) {
		t.Fatalf("a 2.5 s part: %v", err)
	}
}

func TestWhatIsNotAClipIsRefused(t *testing.T) {
	c := videoConverter(t)
	write := func(b []byte) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, In), b, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for name, tc := range map[string]struct {
		dir  string
		want error
	}{
		"text":     {write([]byte("not a clip at all, only words")), ErrUnsupportedVideo},
		"empty":    {write(nil), ErrUnsupportedVideo},
		"heic":     {write(fixture(t, "iphone.heic")), ErrUnsupportedVideo},
		"broken":   {write(append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), bytes.Repeat([]byte{7}, 400)...)), ErrUnreadableVideo},
		"no video": {makeClip(t, "-f", "lavfi", "-i", "sine=d=1", "-c:a", "aac", "-f", "mp4"), ErrUnsupportedVideo},
	} {
		if _, err := c.NormalizeVideo(context.Background(), tc.dir, nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	c.MaxPixels = 1000
	if _, err := c.NormalizeVideo(context.Background(), h264(t, "64x36", "0.5"), nil); !errors.Is(err, ErrVideoTooLarge) {
		t.Errorf("over MaxPixels: %v", err)
	}
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestTheLastFrame(t *testing.T) {
	c := videoConverter(t)
	// Blue, then red: the last frame is red.
	dir := makeClip(t, "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=30:d=1", "-f", "lavfi", "-i", "color=c=red:s=320x180:r=30:d=1",
		"-filter_complex", "[0][1]concat=n=2:v=1[v]", "-map", "[v]", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
	frame, at, err := c.LastFrame(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	img := readPNG(t, frame)
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 180 {
		t.Fatalf("%v", b)
	}
	if px := img.At(160, 90); !near(px, "fe0000") {
		t.Errorf("the last frame is %s, not red", hex(px))
	}
	// Red is one colour, so it is taken 0.35 s before the end.
	if math.Abs(at-(2-fadeBack)) > 0.05 {
		t.Errorf("the frame is at %.3f s, not %.3f", at, 2-fadeBack)
	}
	// A clip that fades to black gives the frame before the fade.
	dir = makeClip(t, "-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=2", "-f", "lavfi", "-i", "color=c=black:s=320x180:r=30:d=0.2",
		"-filter_complex", "[0][1]concat=n=2:v=1[v]", "-map", "[v]", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
	frame, at, err = c.LastFrame(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if flat(readPNG(t, frame)) {
		t.Error("a fade to black gives the black frame")
	}
	if math.Abs(at-(2.2-fadeBack)) > 0.05 {
		t.Errorf("the frame before the fade is at %.3f s, not %.3f", at, 2.2-fadeBack)
	}
	// A rotated clip gives an upright frame.
	src := h264(t, "320x180", "1")
	dir = makeClip(t, "-display_rotation", "90", "-i", filepath.Join(src, In), "-c", "copy", "-f", "mp4")
	frame, at, err = c.LastFrame(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b := readPNG(t, frame).Bounds(); b.Dx() != 180 || b.Dy() != 320 {
		t.Errorf("a rotated clip's frame is %v", b)
	}
	if math.Abs(at-1) > 0.05 {
		t.Errorf("the last frame is at %.3f s, not at the end, 1 s", at)
	}
}

func TestThePoster(t *testing.T) {
	c := videoConverter(t)
	// The test pattern, then blue from 0.5 s: the poster is the first frame.
	dir := makeClip(t, "-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=0.5", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=30:d=1.5",
		"-filter_complex", "[0][1]concat=n=2:v=1[v]", "-map", "[v]", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
	poster, err := c.Poster(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	img := readPNG(t, poster)
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 180 {
		t.Fatalf("%v", b)
	}
	if flat(img) {
		t.Errorf("the poster is %s all over, not the first frame", hex(img.At(160, 90)))
	}
	// A clip that fades in from black gives a frame after the fade.
	dir = makeClip(t, "-f", "lavfi", "-i", "color=c=black:s=320x180:r=30:d=0.3", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=2",
		"-filter_complex", "[0][1]concat=n=2:v=1[v]", "-map", "[v]", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")
	if poster, err = c.Poster(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if flat(readPNG(t, poster)) {
		t.Error("a fade from black gives the black frame")
	}
	// A rotated clip gives an upright poster.
	src := h264(t, "320x180", "1")
	dir = makeClip(t, "-display_rotation", "90", "-i", filepath.Join(src, In), "-c", "copy", "-f", "mp4")
	if poster, err = c.Poster(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if b := readPNG(t, poster).Bounds(); b.Dx() != 180 || b.Dy() != 320 {
		t.Errorf("a rotated clip's poster is %v", b)
	}
}

func TestAResultIsProbed(t *testing.T) {
	c := videoConverter(t)
	dir := h264(t, "640x360", "1.5")
	v, err := c.ProbeVideo(context.Background(), filepath.Join(dir, In))
	if err != nil {
		t.Fatal(err)
	}
	if v.Width != 640 || v.Height != 360 || math.Abs(v.Seconds-1.5) > 0.1 || !v.Audio || v.ContentType != "video/mp4" || v.Bytes == 0 {
		t.Fatalf("%+v", v)
	}
}
