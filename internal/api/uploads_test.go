// SPDX-License-Identifier: Apache-2.0
package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

func TestAPhotoIsKept(t *testing.T) {
	r := newRig(t)
	r.up.asset = storage.Asset{ID: 77, ContentType: "image/jpeg", Bytes: 1234, Width: 800, Height: 600}
	photo := bytes.Repeat([]byte{0xff, 0xd8, 0x01}, 100)
	// The type the client names means nothing: the bytes are read.
	resp := r.do(alice, "POST", "/api/uploads", bytes.NewReader(photo), "Content-Type", "text/plain")
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	want := map[string]any{"id": float64(77), "content_type": "image/jpeg", "bytes": float64(1234), "width": float64(800), "height": float64(600)}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s: %v, want %v", k, m[k], v)
		}
	}
	if !bytes.Equal(r.up.got, photo) || r.up.user != alice {
		t.Errorf("the pipeline got %d bytes for %d", len(r.up.got), r.up.user)
	}
}

func TestAnUploadIsRefusedWithAClearAnswer(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"over the pipeline's limit", storage.ErrTooBig, 413, "too_large"},
		{"over the body cap", &http.MaxBytesError{Limit: 1}, 413, "too_large"},
		{"too many pixels", fmt.Errorf("%w: 9000x9000", media.ErrTooLarge), 413, "picture_too_large"},
		{"not a picture", media.ErrUnsupported, 415, "unsupported_type"},
		{"cut", fmt.Errorf("%w: bad", media.ErrUnreadable), 422, "unreadable"},
		{"anything else", fmt.Errorf("put: %w: secret-token", errBoom), 500, "internal"},
	} {
		r.up.err = c.err
		resp := r.do(alice, "POST", "/api/uploads", strings.NewReader("x"))
		m := jsonOf(t, resp)
		if resp.StatusCode != c.status || m["error"] != c.code {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
		if strings.Contains(fmt.Sprint(m), "secret-token") {
			t.Errorf("%s: %v", c.name, m)
		}
	}
}

func TestAnUploadOverTheCapIsTurnedAway(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.MaxUpload = 1 << 10 } })
	// A body that says how big it is, before a byte is read.
	big := bytes.Repeat([]byte("x"), 2<<20)
	resp := r.do(alice, "POST", "/api/uploads", bytes.NewReader(big))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusRequestEntityTooLarge || m["error"] != "too_large" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
	if r.up.calls != 0 {
		t.Error("the pipeline was started for a body that was over the cap")
	}
	// One that does not (chunked) is cut by the route's own cap.
	resp = r.do(alice, "POST", "/api/uploads", io.MultiReader(bytes.NewReader(big)))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusRequestEntityTooLarge || m["error"] != "too_large" {
		t.Errorf("chunked: %d %v", resp.StatusCode, m)
	}
}

func TestUploadsAreAnswered503WithoutStorage(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Uploads = nil } })
	resp := r.do(alice, "POST", "/api/uploads", strings.NewReader("x"))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || m["error"] != "storage_unavailable" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
}

func TestOnlyAFewPhotosAreConvertedAtOnce(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.cfg = func(c *Config) { c.UploadSlots, c.UploadWait = 1, 100*time.Millisecond }
	})
	r.up.block = make(chan struct{})
	r.up.asset = storage.Asset{ID: 1, ContentType: "image/jpeg"}
	first := make(chan *http.Response, 1)
	go func() { first <- r.do(alice, "POST", "/api/uploads", strings.NewReader("x")) }()
	eventually(t, "the first upload to start", func() bool {
		r.up.mu.Lock()
		defer r.up.mu.Unlock()
		return r.up.calls == 1
	})
	second := r.do(alice, "POST", "/api/uploads", strings.NewReader("x"))
	if m := jsonOf(t, second); second.StatusCode != http.StatusServiceUnavailable || m["error"] != "busy" || second.Header.Get("Retry-After") == "" {
		t.Errorf("%d %v", second.StatusCode, m)
	}
	close(r.up.block)
	if resp := <-first; resp.StatusCode != http.StatusCreated {
		t.Errorf("the first: %d", resp.StatusCode)
	}
	// The slot is free again.
	if resp := r.do(alice, "POST", "/api/uploads", strings.NewReader("x")); resp.StatusCode != http.StatusCreated {
		t.Errorf("after: %d", resp.StatusCode)
	}
}

// A phone on a slow line sends 50 MB in minutes, and the conversion comes
// after: the server's ordinary deadlines are for small JSON.
func TestASlowUploadGetsLongerDeadlines(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.readTimeout, o.writeTimeout = 150*time.Millisecond, 150*time.Millisecond
	})
	r.up.asset = storage.Asset{ID: 5, ContentType: "image/jpeg"}
	r.up.mu.Lock()
	r.up.block = make(chan struct{})
	r.up.mu.Unlock()
	go func() { time.Sleep(300 * time.Millisecond); close(r.up.block) }()
	pr, pw := io.Pipe()
	go func() {
		for range 4 {
			pw.Write([]byte("slow"))
			time.Sleep(100 * time.Millisecond)
		}
		pw.Close()
	}()
	resp := r.do(alice, "POST", "/api/uploads", pr)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if string(r.up.got) != "slowslowslowslow" {
		t.Errorf("%q", r.up.got)
	}
}
