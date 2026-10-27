// SPDX-License-Identifier: Apache-2.0

//go:build r2live

// The live check of a real bucket and its token, run by hand:
//
//	R2_ACCOUNT_ID=… R2_ACCESS_KEY_ID_FILE=… R2_SECRET_ACCESS_KEY_FILE=… \
//	  go test -tags r2live -run Live ./internal/r2
//
// It writes small objects under incoming/r2live-, which the bucket's
// lifecycle clears after a day, and removes them itself. R2_BACKUP_BUCKET
// (default framy-backups) is the second bucket the token must reach;
// R2_FOREIGN_BUCKET, when set, is a bucket of the same account it must not.
// Nothing here prints a credential or a presigned URL.
package r2_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/r2"
)

const (
	siteOrigin     = "https://framy.ws04.cc"
	telegramOrigin = "https://web.telegram.org"
)

func liveFile(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name + "_FILE")
	if path == "" {
		t.Fatalf("%s_FILE is not set", name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s_FILE: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

func liveClient(t *testing.T, bucket string) *r2.Client {
	t.Helper()
	endpoint := os.Getenv("R2_ENDPOINT")
	if account := os.Getenv("R2_ACCOUNT_ID"); account != "" {
		endpoint = "https://" + account + ".r2.cloudflarestorage.com"
	}
	if endpoint == "" {
		t.Fatal("R2_ACCOUNT_ID or R2_ENDPOINT is not set")
	}
	c, err := r2.New(r2.Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		AccessKeyID:     liveFile(t, "R2_ACCESS_KEY_ID"),
		SecretAccessKey: liveFile(t, "R2_SECRET_ACCESS_KEY"),
		Attempts:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func liveKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "incoming/r2live-" + hex.EncodeToString(b) + ".txt"
}

func status(err error) int {
	var e *r2.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// send makes a plain request, as a browser would with a presigned URL.
func send(t *testing.T, method, url string, body []byte, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal("build request")
	}
	if body == nil {
		req.Body = nil
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		// The error carries the URL, and the URL is a credential.
		t.Fatalf("%s failed on the network", method)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, b
}

func TestLiveObjects(t *testing.T) {
	ctx := context.Background()
	c := liveClient(t, envOr("R2_BUCKET", "framy"))
	key, body := liveKey(t), []byte("framy r2live "+time.Now().UTC().Format(time.RFC3339))
	t.Cleanup(func() { _ = c.Delete(ctx, key) })

	if err := c.Put(ctx, key, "text/plain", bytes.NewReader(body), int64(len(body)), sum([]byte("not the body"))); status(err) != http.StatusBadRequest {
		t.Fatalf("a body that does not match its SHA-256: want 400, got %v", err)
	}
	if _, err := c.Head(ctx, key); !errors.Is(err, r2.ErrNotFound) {
		t.Fatalf("the refused put left an object: %v", err)
	}
	if err := c.Put(ctx, key, "text/plain", bytes.NewReader(body), int64(len(body)), sum(body)); err != nil {
		t.Fatalf("put: %v", err)
	}
	obj, err := c.Head(ctx, key)
	if err != nil || obj.Size != int64(len(body)) || obj.ContentType != "text/plain" {
		t.Fatalf("head: %+v, %v", obj, err)
	}
	rc, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("get returned other bytes")
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Head(ctx, key); !errors.Is(err, r2.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// TestLivePresigned is the site's upload and Telegram Web's downloadFile,
// with the Origin headers browsers send, against the bucket's CORS rules.
func TestLivePresigned(t *testing.T) {
	ctx := context.Background()
	c := liveClient(t, envOr("R2_BUCKET", "framy"))
	key, body := liveKey(t), []byte("framy r2live presigned")
	t.Cleanup(func() { _ = c.Delete(ctx, key) })

	put, err := c.PresignPut(key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := send(t, http.MethodOptions, put, nil, map[string]string{
		"Origin":                         siteOrigin,
		"Access-Control-Request-Method":  "PUT",
		"Access-Control-Request-Headers": "content-type",
	})
	if resp.StatusCode/100 != 2 || resp.Header.Get("Access-Control-Allow-Origin") != siteOrigin {
		t.Fatalf("preflight of the site's PUT: %d, allow-origin %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	resp, _ = send(t, http.MethodOptions, put, nil, map[string]string{
		"Origin":                        "https://example.com",
		"Access-Control-Request-Method": "PUT",
	})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a foreign origin may PUT: allow-origin %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
	resp, _ = send(t, http.MethodPut, put, body, map[string]string{"Origin": siteOrigin, "Content-Type": "text/plain"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Access-Control-Allow-Origin") != siteOrigin {
		t.Fatalf("the site's PUT: %d, allow-origin %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if obj, err := c.Head(ctx, key); err != nil || obj.Size != int64(len(body)) {
		t.Fatalf("after the presigned PUT: %+v, %v", obj, err)
	}

	const disposition = `attachment; filename="framy-r2live.txt"`
	get, err := c.PresignGet(key, 5*time.Minute, disposition)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ = send(t, http.MethodOptions, get, nil, map[string]string{
		"Origin":                        telegramOrigin,
		"Access-Control-Request-Method": "GET",
	})
	if resp.StatusCode/100 != 2 || resp.Header.Get("Access-Control-Allow-Origin") != telegramOrigin {
		t.Fatalf("preflight of Telegram Web's GET: %d, allow-origin %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	resp, got := send(t, http.MethodGet, get, nil, map[string]string{"Origin": telegramOrigin})
	switch {
	case resp.StatusCode != http.StatusOK || !bytes.Equal(got, body):
		t.Fatalf("Telegram Web's GET: %d", resp.StatusCode)
	case resp.Header.Get("Content-Disposition") != disposition:
		t.Fatalf("Content-Disposition %q", resp.Header.Get("Content-Disposition"))
	case resp.Header.Get("Access-Control-Allow-Origin") != telegramOrigin:
		t.Fatalf("allow-origin %q", resp.Header.Get("Access-Control-Allow-Origin"))
	case !strings.Contains(strings.ToLower(resp.Header.Get("Access-Control-Expose-Headers")), "content-disposition"):
		t.Fatalf("Content-Disposition is not exposed: %q", resp.Header.Get("Access-Control-Expose-Headers"))
	}

	short, err := c.PresignGet(key, time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if resp, _ := send(t, http.MethodGet, short, nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an expired link: want 403, got %d", resp.StatusCode)
	}
}

// TestLiveScope: the token reaches the backups bucket too, and nothing else.
func TestLiveScope(t *testing.T) {
	ctx := context.Background()
	body := []byte("framy r2live scope")

	backups := liveClient(t, envOr("R2_BACKUP_BUCKET", "framy-backups"))
	key := "r2live/" + strings.TrimPrefix(liveKey(t), "incoming/")
	t.Cleanup(func() { _ = backups.Delete(ctx, key) })
	if err := backups.Put(ctx, key, "text/plain", bytes.NewReader(body), int64(len(body)), sum(body)); err != nil {
		t.Fatalf("put into the backups bucket: %v", err)
	}
	if err := backups.Delete(ctx, key); err != nil {
		t.Fatalf("delete from the backups bucket: %v", err)
	}

	foreign := os.Getenv("R2_FOREIGN_BUCKET")
	if foreign == "" {
		t.Log("R2_FOREIGN_BUCKET is not set: the refusal outside framy's buckets is not checked")
		return
	}
	other := liveClient(t, foreign)
	fkey := liveKey(t)
	if err := other.Put(ctx, fkey, "text/plain", bytes.NewReader(body), int64(len(body)), sum(body)); status(err) != http.StatusForbidden {
		_ = other.Delete(ctx, fkey)
		t.Fatalf("put into %s: want 403, got %v", foreign, err)
	}
	if _, err := other.Head(ctx, fkey); status(err) != http.StatusForbidden {
		t.Fatalf("head in %s: want 403, got %v", foreign, err)
	}
}
