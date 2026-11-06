// SPDX-License-Identifier: Apache-2.0
package storage

import (
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
)

func TestResultType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	webp := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	gif := []byte("GIF89a\x01\x00\x01\x00")
	webm := []byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01")
	mp4 := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2")
	mov := []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x02\x00qt  ")
	for _, c := range []struct {
		name, declared string
		head           []byte
		want           string
	}{
		// The bytes win over a provider that names the wrong type.
		{"a png called webp", "image/webp", png, "image/png"},
		{"a webp called png", "image/png", webp, "image/webp"},
		{"a jpeg called png", "image/png", jpeg, "image/jpeg"},
		{"a jpeg called jpg", "image/jpg", jpeg, "image/jpeg"},
		{"a webm called mp4", "video/mp4", webm, "video/webm"},
		{"a gif called png", "image/png", gif, "image/gif"},
		// Bytes framy cannot tell keep the provider's word.
		{"declared, bytes unknown", "image/bmp", []byte("BM\x00\x00"), "image/bmp"},
		{"declared, text", "text/html", []byte("<html>an error page</html>"), "text/html"},
		{"generic, a png", "application/octet-stream", png, "image/png"},
		{"none, a png", "", png, "image/png"},
		{"none, an mp4", "", mp4, "video/mp4"},
		{"generic, a quicktime", "binary/octet-stream", mov, "video/quicktime"},
		{"none, text", "", []byte("hello"), "text/plain; charset=utf-8"},
	} {
		if got := resultType(c.declared, c.head); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDisposition(t *testing.T) {
	for in, want := range map[string]string{
		"":               "",
		"framy-7.png":    `attachment; filename="framy-7.png"; filename*=UTF-8''framy-7.png`,
		"кот \"x\".jpg":  `attachment; filename="___ _x_.jpg"; filename*=UTF-8''%D0%BA%D0%BE%D1%82%20%22x%22.jpg`,
		"a/b\\c\r\n.jpg": `attachment; filename="a_b_c__.jpg"; filename*=UTF-8''a%2Fb%5Cc%0D%0A.jpg`,
	} {
		if got := disposition(in); got != want {
			t.Errorf("disposition(%q)\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestExt(t *testing.T) {
	for ct, want := range map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "video/mp4": ".mp4", "application/x-thing": ".bin"} {
		if got := ext(ct); got != want {
			t.Errorf("ext(%q) = %q", ct, got)
		}
	}
}

func TestHeadBuffer(t *testing.T) {
	h := &headBuffer{max: 4}
	for _, p := range []string{"ab", "cdef", "gh"} {
		if n, err := h.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatal(n, err)
		}
	}
	if string(h.b) != "abcd" {
		t.Errorf("%q", h.b)
	}
}

func TestNewNeedsItsParts(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("an empty config was accepted")
	}
}

// A provider gets the links to a run's inputs, and a link names its key:
// no key holds the person's Telegram id.
func TestKeysHoldNoPersonsID(t *testing.T) {
	const person = 987654321
	s := &Store{cfg: Config{Prefix: "test/"}}
	j := jobs.Job{ID: 42, UserID: person}
	for _, c := range []struct{ got, want string }{
		{resultKey("", j, 1, "image/png"), "out/42-1.png"},
		{resultKey("test/", j, 0, "video/mp4"), "test/out/42-0.mp4"},
		{joinedKey("", j), "out/42-joined.mp4"},
		{thumbKey(resultKey("", j, 0, "image/png")), "out/42-0.thumb.webp"},
	} {
		if c.got != c.want {
			t.Errorf("%s, want %s", c.got, c.want)
		}
	}
	up := s.uploadKey("image/jpeg")
	if !strings.HasPrefix(up, "test/in/") || !strings.HasSuffix(up, ".jpg") || len(up) != len("test/in/")+32+len(".jpg") {
		t.Errorf("an upload goes to %s", up)
	}
	if again := s.uploadKey("image/jpeg"); again == up {
		t.Error("two uploads got one key")
	}
}
