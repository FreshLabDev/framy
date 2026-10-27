// SPDX-License-Identifier: Apache-2.0
package storage

import (
	"testing"
)

func TestResultType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	mp4 := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2")
	mov := []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x02\x00qt  ")
	for _, c := range []struct {
		name, declared string
		head           []byte
		want           string
	}{
		{"declared", "image/webp", png, "image/webp"},
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
