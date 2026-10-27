// SPDX-License-Identifier: Apache-2.0

// Package gemini runs generations on a person's own Gemini key, through the
// Interactions API (docs/design.md, Jobs). It is the only door to what
// OpenRouter does not pass on: thinking_level and Google Search grounding.
// An image run is one synchronous call that answers with the picture, so
// there is nothing to poll. Video (Gemini Omni) comes later.
//
// The adapter tells three kinds of "no" apart, because the queue and the
// person do different things with each:
//
//   - a refusal (class moderation, *Refusal): a filter blocked the prompt or
//     the image, or the model answered in words and drew nothing;
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
	// DefaultMaxResponse bounds an answer: a 4K PNG is about 30 MB in base64.
	DefaultMaxResponse = 128 << 20
	// DefaultMaxInput bounds one reference, and DefaultMaxInputs all of them
	// together. Google takes 100 MB of inline data in a request, and base64
	// adds a third. Photos reach this point already at most 2048 px long.
	DefaultMaxInput  = 20 << 20
	DefaultMaxInputs = 48 << 20
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
	// Fetch downloads the references from their presigned links. It must
	// refuse private addresses; the default is safehttp's client.
	Fetch *http.Client
	// Key returns the person's Gemini key for the job. An *jobs.Error keeps
	// its class; any other error fails the job as internal. The key never
	// reaches a log or an error.
	Key func(ctx context.Context, j jobs.Job) (string, error)
	// MaxResponse bounds an answer, DefaultMaxResponse when zero.
	MaxResponse int64
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
	if cfg.MaxInput <= 0 {
		cfg.MaxInput = DefaultMaxInput
	}
	if cfg.MaxInputs <= 0 {
		cfg.MaxInputs = DefaultMaxInputs
	}
	return &Provider{cfg: cfg}
}

// Submit runs an image. Everything framy can check is checked before the key
// is asked for and before a reference is fetched, so a run refused for its
// settings costs nothing.
func (p *Provider) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	if j.Kind != catalog.Image {
		return jobs.Submission{}, &jobs.Error{Class: jobs.NoAdapter, Err: errors.New("framy cannot run Gemini video yet")}
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
	out, err := p.interact(ctx, key, j.Offer, body)
	if err != nil {
		return jobs.Submission{}, err
	}
	return jobs.Submission{Outcome: &out}, nil
}

// Poll is never called for an image: Submit returns the result.
func (p *Provider) Poll(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	return jobs.Outcome{}, &jobs.Error{Class: jobs.NoAdapter, Err: fmt.Errorf("Gemini %s runs are not polled yet", j.Kind)}
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

// file is a reference, fetched.
type file struct {
	contentType string
	data        []byte
}

// fetchAll downloads the references from their presigned links. Gemini would
// take the link itself only if it were a Files API one; the bytes go inline.
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
			return nil, refused("the references together are larger than %d MiB", p.cfg.MaxInputs>>20)
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

// interact sends the paid request.
func (p *Provider) interact(ctx context.Context, key string, of catalog.Offer, body []byte) (jobs.Outcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Base+"/interactions", bytes.NewReader(body))
	if err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	// The key goes in a header and never in the URL, which logs keep.
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Api-Revision", p.cfg.Revision)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return jobs.Outcome{}, transportError(err)
	}
	defer resp.Body.Close()

	limited := http.MaxBytesReader(nil, resp.Body, p.cfg.MaxResponse)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(limited, 1<<20))
		return jobs.Outcome{}, statusError(resp.StatusCode, resp.Header, data, key)
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		if ctx.Err() != nil {
			// Cut short by the deadline or a stop: the queue reads which.
			return jobs.Outcome{}, fmt.Errorf("read the answer of gemini: %w", err)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = fmt.Errorf("the answer is larger than %d MiB", p.cfg.MaxResponse>>20)
		}
		// The run was likely paid, so this is not tried again.
		return jobs.Outcome{}, &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of gemini: %w", err)}
	}
	return outcome(of, raw, key), nil
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
