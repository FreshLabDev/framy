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
// R2_INCOMING_BUCKET, with R2_INCOMING_ACCESS_KEY_ID_FILE and
// R2_INCOMING_SECRET_ACCESS_KEY_FILE, is the clips' bucket and its token,
// which must reach nothing else. Nothing here prints a credential or a
// presigned URL.
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
	testSiteOrigin = "https://framy-test.ws04.cc"
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
	return liveClientAs(t, bucket, "R2")
}

// liveIncoming is the clips' bucket with its own token, or a skip.
func liveIncoming(t *testing.T) (*r2.Client, string) {
	t.Helper()
	bucket := os.Getenv("R2_INCOMING_BUCKET")
	if bucket == "" {
		t.Skip("R2_INCOMING_BUCKET is not set: clips go into the main bucket")
	}
	return liveClientAs(t, bucket, "R2_INCOMING"), bucket
}

// liveClientAs reaches bucket with the token in <prefix>_ACCESS_KEY_ID_FILE
// and <prefix>_SECRET_ACCESS_KEY_FILE.
func liveClientAs(t *testing.T, bucket, prefix string) *r2.Client {
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
		AccessKeyID:     liveFile(t, prefix+"_ACCESS_KEY_ID"),
		SecretAccessKey: liveFile(t, prefix+"_SECRET_ACCESS_KEY"),
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

	// The site's links are signed at the start of a window and tell the
	// browser to keep the answer.
	const cache = "private, max-age=4200"
	kept, err := c.PresignGetAt(key, time.Now().Truncate(time.Hour), 70*time.Minute, "", cache)
	if err != nil {
		t.Fatal(err)
	}
	if resp, got := send(t, http.MethodGet, kept, nil, map[string]string{"Origin": siteOrigin}); resp.StatusCode != http.StatusOK ||
		!bytes.Equal(got, body) || resp.Header.Get("Cache-Control") != cache {
		t.Fatalf("a link signed at the window's start: %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
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

// copied tells whether a presigned PUT to dst, sent with an
// x-amz-copy-source header the signature does not cover, made dst a copy
// of source. It answers the PUT's status too, for the log.
func copied(t *testing.T, ctx context.Context, link string, dst *r2.Client, dstKey, source string, secret []byte) (bool, int) {
	t.Helper()
	resp, _ := send(t, http.MethodPut, link, []byte("plain"), map[string]string{"x-amz-copy-source": source})
	rc, err := dst.Get(ctx, dstKey)
	if err != nil {
		return false, resp.StatusCode
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	return bytes.Equal(got, secret), resp.StatusCode
}

// TestLiveCopySource: the link the site's browser puts a clip with signs
// the host alone, so its holder may add any header. One must not make R2
// copy a file of someone else's into their key: it would become an upload
// of theirs, which they can download. With R2_INCOMING_BUCKET set framy
// gives no such link for the main bucket, and a copy here is only logged.
func TestLiveCopySource(t *testing.T) {
	ctx := context.Background()
	bucket := envOr("R2_BUCKET", "framy")
	c := liveClient(t, bucket)
	src, secret := liveKey(t), []byte("framy r2live someone else's file")
	t.Cleanup(func() { _ = c.Delete(ctx, src) })
	if err := c.Put(ctx, src, "text/plain", bytes.NewReader(secret), int64(len(secret)), sum(secret)); err != nil {
		t.Fatalf("put the source: %v", err)
	}
	dst := liveKey(t)
	t.Cleanup(func() { _ = c.Delete(ctx, dst) })
	link, err := c.PresignPut(dst, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ok, code := copied(t, ctx, link, c, dst, bucket+"/"+src, secret)
	switch {
	case !ok:
		t.Logf("a presigned PUT with x-amz-copy-source within %s: %d, no copy", bucket, code)
	case os.Getenv("R2_INCOMING_BUCKET") != "":
		// framy signs no PUT with this token then: TestLiveIncoming checks
		// the clips' bucket refuses the copy.
		t.Logf("R2 copied a file on a presigned PUT within %s (%d); clips go into %s, whose token reaches nothing else", bucket, code, os.Getenv("R2_INCOMING_BUCKET"))
	default:
		t.Fatalf("R2 copied a file on a presigned PUT within %s (%d): clips need a bucket and a token of their own (R2_INCOMING_BUCKET)", bucket, code)
	}
}

// TestLiveIncoming: the clips' bucket takes the site's PUT, and its token
// reaches nothing else: no file of the main bucket can be read with it,
// nor copied in.
func TestLiveIncoming(t *testing.T) {
	ctx := context.Background()
	clips, clipsBucket := liveIncoming(t)
	bucket := envOr("R2_BUCKET", "framy")
	main := liveClient(t, bucket)

	key, body := liveKey(t), []byte("framy r2live clip")
	t.Cleanup(func() { _ = clips.Delete(ctx, key) })
	link, err := clips.PresignPut(key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{siteOrigin, testSiteOrigin} {
		resp, _ := send(t, http.MethodOptions, link, nil, map[string]string{
			"Origin":                         origin,
			"Access-Control-Request-Method":  "PUT",
			"Access-Control-Request-Headers": "content-type",
		})
		if resp.StatusCode/100 != 2 || resp.Header.Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("preflight of %s's PUT into %s: %d, allow-origin %q", origin, clipsBucket, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}
	}
	resp, _ := send(t, http.MethodPut, link, body, map[string]string{"Origin": siteOrigin, "Content-Type": "video/mp4"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Access-Control-Allow-Origin") != siteOrigin {
		t.Fatalf("the site's PUT into %s: %d, allow-origin %q", clipsBucket, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if obj, err := clips.Head(ctx, key); err != nil || obj.Size != int64(len(body)) {
		t.Fatalf("after the PUT: %+v, %v", obj, err)
	}

	src, secret := liveKey(t), []byte("framy r2live a file of the main bucket")
	t.Cleanup(func() { _ = main.Delete(ctx, src) })
	if err := main.Put(ctx, src, "text/plain", bytes.NewReader(secret), int64(len(secret)), sum(secret)); err != nil {
		t.Fatalf("put into %s: %v", bucket, err)
	}
	reach := liveClientAs(t, bucket, "R2_INCOMING")
	if _, err := reach.Head(ctx, src); status(err) != http.StatusForbidden {
		t.Fatalf("the clips' token in %s: want 403, got %v", bucket, err)
	}
	dst := liveKey(t)
	t.Cleanup(func() { _ = clips.Delete(ctx, dst) })
	copyLink, err := clips.PresignPut(dst, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, code := copied(t, ctx, copyLink, clips, dst, bucket+"/"+src, secret); ok {
		t.Fatalf("a file of %s was copied into %s (%d)", bucket, clipsBucket, code)
	}
}
