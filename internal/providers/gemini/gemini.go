// SPDX-License-Identifier: Apache-2.0

// Package gemini runs generations on a person's own Gemini key, through the
// Interactions API (docs/design.md, Jobs). It is the only door to what
// OpenRouter does not pass on: thinking_level, Google Search and Image Search
// grounding, and Gemini Omni, the one video model a Gemini key has. An image
// run is one synchronous call that answers with the picture, so there is
// nothing to poll. A video runs in the background: Submit returns the
// interaction's id and Poll asks about it until the video is in the answer.
//
// The adapter tells three kinds of "no" apart, because the queue and the
// person do different things with each:
//
//   - a refusal (class moderation, *Refusal): a filter blocked the prompt,
//     the picture or the video, or the model answered in words and made
//     nothing;
//   - a key that cannot pay (class key or money, *KeyError): no billing on
//     the project, a spent prepaid balance, a wrong key. None is sent again;
//   - a failure, which is retried only when it came before the run started.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/safehttp"
)

const (
	// DefaultBase is the Gemini API.
	DefaultBase = "https://generativelanguage.googleapis.com/v1beta"
	// DefaultRevision is the schema of the Interactions API framy speaks,
	// pinned: the steps schema of 20 May 2026. Google has removed the older
	// one and ignores the header now, but a later revision will not be taken
	// silently.
	DefaultRevision = "2026-05-20"
	// DefaultMaxResponse bounds an image's answer: a 4K PNG is about 30 MB in
	// base64.
	DefaultMaxResponse = 128 << 20
	// DefaultMaxVideo bounds a finished video, which is held in memory while
	// it is copied, the same 128 MiB as OpenRouter's (docs/design.md, Jobs):
	// ten seconds of Omni at 4K is well under it. A video comes inline in
	// base64, a third larger, so its answer may be up to
	// DefaultMaxVideoAnswer.
	DefaultMaxVideo       = 128 << 20
	DefaultMaxVideoAnswer = 192 << 20
	// DefaultMaxInput bounds one file sent, and DefaultMaxInputs all of them
	// together. Google takes 100 MB of inline data in a request, and base64
	// adds a third. Photos reach this point already at most 2048 px long; a
	// video to edit is up to 10 s.
	DefaultMaxInput  = 48 << 20
	DefaultMaxInputs = 60 << 20
	// DefaultFetchTimeout bounds the download of one reference.
	DefaultFetchTimeout = 60 * time.Second
)

// Config is how the adapter reaches Gemini.
type Config struct {
	// Base is the API root, DefaultBase when empty.
	Base string
	// HTTP makes the calls to Gemini. The queue's context bounds each one,
	// so the client needs no timeout of its own.
	HTTP      *http.Client
	UserAgent string
	// Revision is the Api-Revision header, DefaultRevision when empty.
	Revision string
	// Fetch downloads the references from their presigned links, and a
	// video Google hands over as a file. It must refuse private addresses;
	// the default is safehttp's client.
	Fetch *http.Client
	// Key returns the person's Gemini key for the job. An *jobs.Error keeps
	// its class; any other error fails the job as internal. The key never
	// reaches a log or an error.
	Key func(ctx context.Context, j jobs.Job) (string, error)
	// MaxResponse bounds an image's answer, DefaultMaxResponse when zero.
	MaxResponse int64
	// MaxVideo bounds a finished video and MaxVideoAnswer the answer that
	// carries it; the defaults when zero.
	MaxVideo, MaxVideoAnswer int64
	// MaxInput and MaxInputs bound one reference and all of them, in bytes
	// before base64; the defaults when zero.
	MaxInput, MaxInputs int64
}

// Provider is the Gemini adapter of the job queue.
type Provider struct {
	cfg Config
}

// New builds the adapter.
func New(cfg Config) *Provider {
	cfg.Base = strings.TrimRight(cfg.Base, "/")
	if cfg.Base == "" {
		cfg.Base = DefaultBase
	}
	if cfg.Revision == "" {
		cfg.Revision = DefaultRevision
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Fetch == nil {
		cfg.Fetch = safehttp.Client(DefaultFetchTimeout)
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = DefaultMaxResponse
	}
	if cfg.MaxVideo <= 0 {
		cfg.MaxVideo = DefaultMaxVideo
	}
	if cfg.MaxVideoAnswer <= 0 {
		cfg.MaxVideoAnswer = DefaultMaxVideoAnswer
	}
	if cfg.MaxInput <= 0 {
		cfg.MaxInput = DefaultMaxInput
	}
	if cfg.MaxInputs <= 0 {
		cfg.MaxInputs = DefaultMaxInputs
	}
	return &Provider{cfg: cfg}
}

// Submit runs an image, or starts a video. Everything framy can check is
// checked before the key is asked for and before a file is fetched, so a run
// refused for its settings costs nothing.
func (p *Provider) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	if j.Kind != catalog.Image && j.Kind != catalog.Video {
		return jobs.Submission{}, &jobs.Error{Class: jobs.NoAdapter, Err: fmt.Errorf("framy cannot run a Gemini %s", j.Kind)}
	}
	pl, err := planFor(j)
	if err != nil {
		return jobs.Submission{}, err
	}
	key, err := p.key(ctx, j)
	if err != nil {
		return jobs.Submission{}, err
	}
	files, err := p.fetchAll(ctx, pl.inputs)
	if err != nil {
		return jobs.Submission{}, err
	}
	body, err := pl.body(files)
	if err != nil {
		return jobs.Submission{}, err
	}
	if j.Kind == catalog.Video {
		return p.startVideo(ctx, key, j, body)
	}
	raw, err := p.do(ctx, key, http.MethodPost, "/interactions", body, p.cfg.MaxResponse)
	if err != nil {
		return jobs.Submission{}, err
	}
	out := outcome(j.Offer, raw, key)
	return jobs.Submission{Outcome: &out}, nil
}

// Poll asks about a video. An image is never polled: Submit returns it.
func (p *Provider) Poll(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	if j.Kind != catalog.Video {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.NoAdapter, Err: fmt.Errorf("Gemini %s runs are not polled", j.Kind)}
	}
	return p.pollVideo(ctx, j)
}

func (p *Provider) key(ctx context.Context, j jobs.Job) (string, error) {
	if p.cfg.Key == nil {
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no Gemini key is configured")}
	}
	key, err := p.cfg.Key(ctx, j)
	var pe *jobs.Error
	switch {
	case errors.As(err, &pe):
		return "", pe
	case err != nil:
		return "", &jobs.Error{Class: jobs.Internal, Err: fmt.Errorf("find the key: %w", err)}
	case key == "":
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no Gemini key pays for this job")}
	}
	return key, nil
}

// file is an input, fetched.
type file struct {
	contentType string
	data        []byte
}

// fetchAll downloads the inputs from their presigned links. Gemini would take
// the link itself only if it were a Files API one; the bytes go inline.
// Nothing is paid yet, so a failure here is safe to try again, except for a
// link that is gone.
func (p *Provider) fetchAll(ctx context.Context, inputs []jobs.Input) ([]file, error) {
	var files []file
	var total int64
	for i, in := range inputs {
		data, err := p.fetch(ctx, in, i)
		if err != nil {
			return nil, err
		}
		if total += int64(len(data)); total > p.cfg.MaxInputs {
			return nil, refused("the files together are larger than %d MiB", p.cfg.MaxInputs>>20)
		}
		files = append(files, file{contentType: in.ContentType, data: data})
	}
	return files, nil
}

func (p *Provider) fetch(ctx context.Context, in jobs.Input, i int) ([]byte, error) {
	if rest, ok := strings.CutPrefix(in.URL, "data:"); ok {
		// A data URL, as the tests and the live check make.
		_, b64, found := strings.Cut(rest, ",")
		data, err := base64.StdEncoding.DecodeString(b64)
		if !found || err != nil || len(data) == 0 {
			return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d is a broken data URL", i+1)}
		}
		if int64(len(data)) > p.cfg.MaxInput {
			return nil, refused("input %d is larger than %d MiB", i+1, p.cfg.MaxInput>>20)
		}
		return data, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no usable link", i+1)}
	}
	resp, err := p.cfg.Fetch.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// A deadline or a stop: the queue reads the context.
			return nil, fmt.Errorf("fetch input %d: %w", i+1, withoutURL(err))
		}
		// The error would carry the presigned link, which opens the person's
		// file to whoever reads the log.
		return nil, &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("fetch input %d: %w", i+1, withoutURL(err))}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("fetch input %d: storage answered %d", i+1, resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d is gone or its link expired: storage answered %d", i+1, resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxInput+1))
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, fmt.Errorf("fetch input %d: %w", i+1, withoutURL(err))
	case err != nil:
		return nil, &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("fetch input %d: %w", i+1, withoutURL(err))}
	case int64(len(data)) > p.cfg.MaxInput:
		return nil, refused("input %d is larger than %d MiB", i+1, p.cfg.MaxInput>>20)
	case len(data) == 0:
		return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d is empty", i+1)}
	}
	return data, nil
}

// withoutURL strips the URL a *url.Error carries.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// tooLargeError: an answer, or a video in it, above its limit.
type tooLargeError struct {
	what  string
	limit int64
}

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("%s is larger than %d MiB", e.what, e.limit>>20)
}

// do sends a request to Gemini with the key and reads a 200's body, up to
// limit bytes. Any other status is read as Google's error. A body that breaks
// off or is too large fails as provider: after a POST the run was likely
// paid, so it is not sent again; after a GET the queue asks again.
func (p *Provider) do(ctx context.Context, key, method, path string, body []byte, limit int64) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.Base+path, rd)
	if err != nil {
		return nil, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	p.authorize(req, key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()

	limited := http.MaxBytesReader(nil, resp.Body, limit)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(limited, 1<<20))
		return nil, statusError(resp.StatusCode, resp.Header, data, key)
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		if ctx.Err() != nil {
			// Cut short by the deadline or a stop: the queue reads which.
			return nil, fmt.Errorf("read the answer of gemini: %w", err)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = &tooLargeError{what: "the answer", limit: limit}
		}
		return nil, &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of gemini: %w", err)}
	}
	return raw, nil
}

// authorize puts the key and the pinned revision on a request to Gemini. The
// key goes in a header and never in the URL, which logs keep.
func (p *Provider) authorize(req *http.Request, key string) {
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Api-Revision", p.cfg.Revision)
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
}

// transportError reads a failed call. A connection that was never made sent
// nothing, so it is safe to try again; anything later may have been paid.
func transportError(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("reach gemini: %w", withoutURL(err))}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("reach gemini: %w", withoutURL(err))}
	}
	// A deadline or a stop: the queue reads the context.
	return fmt.Errorf("call gemini: %w", withoutURL(err))
}
