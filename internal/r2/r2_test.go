// SPDX-License-Identifier: Apache-2.0
package r2_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/r2"
	"github.com/FreshLabDev/framy/internal/r2/r2test"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func put(t *testing.T, c *r2.Client, key string, data []byte) error {
	t.Helper()
	return c.Put(context.Background(), key, "image/jpeg", bytes.NewReader(data), int64(len(data)), sum(data))
}

func TestPutHeadGetDelete(t *testing.T) {
	srv := r2test.New(t)
	c := srv.Client(t)
	ctx := context.Background()
	data := []byte("a picture")
	for _, key := range []string{"u/1/out/7-0.jpg", "u/1/in/a b+c$.jpg", "u/1/in/кот.jpg"} {
		if err := put(t, c, key, data); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		o, ok := srv.Get(key)
		if !ok || !bytes.Equal(o.Data, data) || o.ContentType != "image/jpeg" {
			t.Fatalf("%q is %+v %v", key, o, ok)
		}
		h, err := c.Head(ctx, key)
		if err != nil || h.Size != int64(len(data)) || h.ContentType != "image/jpeg" {
			t.Errorf("head %q: %+v %v", key, h, err)
		}
		rc, err := c.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, data) {
			t.Errorf("get %q: %q", key, got)
		}
		if err := c.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Head(ctx, key); !errors.Is(err, r2.ErrNotFound) {
			t.Errorf("head after delete: %v, want ErrNotFound", err)
		}
	}
	if err := c.Delete(ctx, "u/1/never"); err != nil {
		t.Errorf("deleting what is not there: %v", err)
	}
	if _, err := c.Get(ctx, "u/1/never"); !errors.Is(err, r2.ErrNotFound) {
		t.Errorf("get of nothing: %v", err)
	}
}

func TestABodyThatDoesNotMatchItsHashIsRefused(t *testing.T) {
	srv := r2test.New(t)
	c := srv.Client(t)
	data := []byte("a picture")
	err := c.Put(context.Background(), "u/1/x.jpg", "image/jpeg", bytes.NewReader(data), int64(len(data)), sum([]byte("another")))
	var e *r2.Error
	if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Code != "XAmzContentSHA256Mismatch" {
		t.Fatalf("%v, want a 400 mismatch", err)
	}
	if n := srv.Calls(http.MethodPut); n != 1 {
		t.Errorf("a refusal was sent %d times", n)
	}
	if len(srv.Keys()) != 0 {
		t.Error("the object was kept")
	}
}

func TestA5xxIsTriedAgain(t *testing.T) {
	srv := r2test.New(t)
	c := srv.Client(t)
	srv.FailNext(http.StatusServiceUnavailable)
	if err := put(t, c, "u/1/a.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if n := srv.Calls(http.MethodPut); n != 2 {
		t.Errorf("%d puts, want 2", n)
	}
	srv.FailNext(500, 502, 503)
	err := put(t, c, "u/1/b.jpg", []byte("x"))
	var e *r2.Error
	if !errors.As(err, &e) || !e.Temporary() {
		t.Fatalf("%v, want the last 5xx", err)
	}
	if _, ok := srv.Get("u/1/b.jpg"); ok {
		t.Error("kept after three failures")
	}
}

func TestAWrongSecretIsRefusedAndNotRetried(t *testing.T) {
	srv := r2test.New(t)
	cfg := srv.Config()
	cfg.SecretAccessKey = "not-the-secret"
	c, err := r2.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = put(t, c, "u/1/a.jpg", []byte("x"))
	var e *r2.Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("%v, want 403", err)
	}
	if n := srv.Calls(http.MethodPut); n != 1 {
		t.Errorf("%d puts, want 1", n)
	}
	if strings.Contains(err.Error(), "not-the-secret") {
		t.Error("the error names the secret")
	}
}

func TestPresignedLinks(t *testing.T) {
	srv := r2test.New(t)
	c := srv.Client(t)
	data := []byte("a result")
	if err := put(t, c, "u/1/out/7-0.jpg", data); err != nil {
		t.Fatal(err)
	}
	link, err := c.PresignGet("u/1/out/7-0.jpg", 10*time.Minute, `attachment; filename="framy-7.jpg"`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(link, r2test.Secret) {
		t.Fatal("the link holds the secret")
	}
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, data) || resp.Header.Get("Content-Disposition") != `attachment; filename="framy-7.jpg"` {
		t.Errorf("%d %q %q", resp.StatusCode, got, resp.Header.Get("Content-Disposition"))
	}
	// The link names one object: another key under it is refused.
	other := strings.Replace(link, "7-0.jpg", "8-0.jpg", 1)
	if resp, err := http.Get(other); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("another key with the same signature: %v %v", resp.StatusCode, err)
	}
	// And it dies on time.
	srv.SetNow(func() time.Time { return time.Now().Add(10*time.Minute + time.Second) })
	if resp, err := http.Get(link); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("an expired link: %v %v", resp.StatusCode, err)
	}
	srv.SetNow(time.Now)

	up, err := c.PresignPut("incoming/1/clip.mp4", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, up, strings.NewReader("a video"))
	req.Header.Set("Content-Type", "video/mp4")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("a presigned put: %v %v", resp.StatusCode, err)
	}
	if o, ok := srv.Get("incoming/1/clip.mp4"); !ok || string(o.Data) != "a video" {
		t.Errorf("the upload is %+v", o)
	}
	for _, ttl := range []time.Duration{0, 8 * 24 * time.Hour} {
		if _, err := c.PresignGet("k", ttl, ""); err == nil {
			t.Errorf("a link for %s", ttl)
		}
	}
}

func TestConfigIsChecked(t *testing.T) {
	good := r2.Config{Endpoint: "https://acc.r2.cloudflarestorage.com", Bucket: "framy", AccessKeyID: "a", SecretAccessKey: "s"}
	if _, err := r2.New(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*r2.Config){
		"no endpoint":   func(c *r2.Config) { c.Endpoint = "" },
		"ftp":           func(c *r2.Config) { c.Endpoint = "ftp://acc.r2.cloudflarestorage.com" },
		"a query":       func(c *r2.Config) { c.Endpoint += "?x=1" },
		"no bucket":     func(c *r2.Config) { c.Bucket = "" },
		"a slash":       func(c *r2.Config) { c.Bucket = "a/b" },
		"no secret":     func(c *r2.Config) { c.SecretAccessKey = "" },
		"no access key": func(c *r2.Config) { c.AccessKeyID = "" },
	} {
		cfg := good
		mutate(&cfg)
		if _, err := r2.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
