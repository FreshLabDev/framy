// SPDX-License-Identifier: Apache-2.0

// Package r2 talks to a Cloudflare R2 bucket through its S3 API: put,
// head and delete an object, and presign a GET or a PUT. framy keeps every
// input and result there (docs/design.md, Storage and retention).
//
// The credentials go into signatures only. A presigned URL is a bearer
// token for one object until it expires: it goes to the object's owner or
// to a provider for one call, and never into a log.
package r2

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxPresign is the longest a presigned URL lives, by the S3 API.
const MaxPresign = 7 * 24 * time.Hour

// ErrNotFound: no object under the key.
var ErrNotFound = errors.New("no such object")

// Config is a bucket and the token that reaches it.
type Config struct {
	// Endpoint is the account's S3 endpoint,
	// https://<account>.r2.cloudflarestorage.com.
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// Region is "auto" for R2.
	Region string
	HTTP   *http.Client
	// Attempts is how many times a call that failed with a 5xx or on the
	// network is made; every call here is idempotent.
	Attempts int
	// Now is the clock, for tests.
	Now func() time.Time
}

// Client is one bucket.
type Client struct {
	base     *url.URL
	bucket   string
	signer   signer
	http     *http.Client
	attempts int
	now      func() time.Time
}

// New checks cfg and returns its client.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.Endpoint, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("r2: the endpoint must be an https URL with no path or query")
	}
	if cfg.Bucket == "" || strings.Contains(cfg.Bucket, "/") {
		return nil, errors.New("r2: a bucket name is needed")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("r2: the access key id and the secret are needed")
	}
	c := &Client{
		base:     u,
		bucket:   cfg.Bucket,
		signer:   signer{accessKeyID: cfg.AccessKeyID, secret: cfg.SecretAccessKey, region: or(cfg.Region, "auto")},
		http:     cfg.HTTP,
		attempts: cfg.Attempts,
		now:      cfg.Now,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 2 * time.Minute}
	}
	if c.attempts <= 0 {
		c.attempts = 3
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Bucket names the client's bucket.
func (c *Client) Bucket() string { return c.bucket }

// object is the path-style URL of key.
func (c *Client) object(key string) *url.URL {
	u := *c.base
	u.Path = u.Path + "/" + c.bucket + "/" + key
	u.RawPath = encode(u.Path, false)
	return &u
}

// Error is an answer of the S3 API that is not a success.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("r2: %d", e.Status)
	}
	return fmt.Sprintf("r2: %d %s: %s", e.Status, e.Code, e.Message)
}

// Temporary: a 5xx or a 429, which the next attempt may not see.
func (e *Error) Temporary() bool { return e.Status >= 500 || e.Status == http.StatusTooManyRequests }

// Put stores size bytes of body under key. sum is the body's SHA-256 in hex:
// R2 refuses a body that does not match it, so a cut upload never becomes
// an object.
func (c *Client) Put(ctx context.Context, key, contentType string, body io.ReaderAt, size int64, sum string) error {
	_, err := c.do(ctx, http.MethodPut, key, false, func(req *http.Request) {
		req.Body = io.NopCloser(io.NewSectionReader(body, 0, size))
		req.ContentLength = size
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(body, 0, size)), nil
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-Amz-Content-Sha256", sum)
	}, "content-type")
	return err
}

// Object is what HEAD says of an object.
type Object struct {
	Size        int64
	ContentType string
}

// Head asks about key; ErrNotFound when there is nothing.
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	resp, err := c.do(ctx, http.MethodHead, key, false, nil)
	if err != nil {
		return Object{}, err
	}
	return Object{Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}, nil
}

// Delete removes key. An object that is not there counts as removed.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, http.MethodDelete, key, false, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Get opens key for reading; the caller closes it.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, key, true, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// PresignGet links to key for ttl. disposition, when set, comes back as the
// Content-Disposition of the answer: "attachment; filename=…" makes a
// browser or Telegram's downloadFile save it.
func (c *Client) PresignGet(key string, ttl time.Duration, disposition string) (string, error) {
	u := c.object(key)
	if disposition != "" {
		u.RawQuery = url.Values{"response-content-disposition": {disposition}}.Encode()
	}
	return c.presign(http.MethodGet, u, ttl)
}

// PresignPut lets the holder upload to key for ttl, as the site does with a
// video, straight into the bucket.
func (c *Client) PresignPut(key string, ttl time.Duration) (string, error) {
	return c.presign(http.MethodPut, c.object(key), ttl)
}

func (c *Client) presign(method string, u *url.URL, ttl time.Duration) (string, error) {
	if ttl < time.Second || ttl > MaxPresign {
		return "", fmt.Errorf("r2: a presigned URL lives from a second to %s, not %s", MaxPresign, ttl)
	}
	return c.signer.presign(method, u, c.now(), ttl), nil
}

// do makes a signed call, again after a 5xx, a 429 or a network error. The
// answer's body is read and closed unless keep, when the caller closes it.
func (c *Client) do(ctx context.Context, method, key string, keep bool, prepare func(*http.Request), extra ...string) (*http.Response, error) {
	var last error
	for attempt := range c.attempts {
		if attempt > 0 {
			wait := time.Duration(attempt*attempt) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.object(key).String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
		if prepare != nil {
			prepare(req)
		}
		c.signer.sign(req, c.now(), extra...)
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// The error names the URL, which holds no credentials: the
			// signature is in a header.
			last = fmt.Errorf("r2: %s %s: %w", method, key, err)
			continue
		}
		if resp.StatusCode/100 == 2 {
			if !keep {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
			}
			return resp, nil
		}
		e := readError(resp)
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		if !e.Temporary() {
			return nil, e
		}
		last = e
	}
	return nil, last
}

// readError reads the XML error of an answer and closes its body.
func readError(resp *http.Response) *Error {
	defer resp.Body.Close()
	e := &Error{Status: resp.StatusCode}
	var body struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); err == nil && xml.Unmarshal(raw, &body) == nil {
		e.Code, e.Message = body.Code, body.Message
	}
	return e
}
