// SPDX-License-Identifier: Apache-2.0

// Package openrouter runs generations on OpenRouter. Images go through
// POST /api/v1/images, which answers with the image itself, so a run is one
// call. Video goes through POST /api/v1/videos, which answers with a job to
// poll, and a webhook (Hook) that brings the poll forward (docs/design.md,
// Jobs).
package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// DefaultBase is OpenRouter's API.
const DefaultBase = "https://openrouter.ai/api/v1"

// DefaultMaxResponse bounds an answer: a 4K PNG is about 30 MB in base64,
// and a run may return several.
const DefaultMaxResponse = 256 << 20

// DefaultMaxVideo bounds a finished video, which is held in memory while it
// is copied to R2. Four video workers then hold 512 MiB at most; a 10 s 1080p
// clip of today's models is 10 to 30 MB.
const DefaultMaxVideo = 128 << 20

// AppURL and AppTitle are how framy appears in OpenRouter (HTTP-Referer and
// X-Title): the production bot, from every stack, the test one included, so
// the activity and the app rankings point people at the product and not at
// the repository (owner, 28 October).
const (
	AppURL   = "https://t.me/framyin_bot"
	AppTitle = "framy"
)

// Config is how the adapter reaches OpenRouter.
type Config struct {
	// Base is the API root, DefaultBase when empty.
	Base string
	// HTTP makes the calls. The queue's context bounds each one, so the
	// client needs no timeout of its own.
	HTTP      *http.Client
	UserAgent string
	// Referer and Title name framy in OpenRouter's app rankings
	// (HTTP-Referer and X-Title).
	Referer string
	Title   string
	// Key returns the key that pays for the job: framy's own for the free
	// allowance, the person's connection otherwise. An *jobs.Error keeps its
	// class; any other error fails the job as internal. The key never reaches
	// a log or an error.
	Key func(ctx context.Context, j jobs.Job) (string, error)
	// MaxResponse bounds an answer: DefaultMaxResponse when zero.
	MaxResponse int64
	// MaxVideo bounds a finished video: DefaultMaxVideo when zero. A larger
	// one fails the run as Storage, after it was paid.
	MaxVideo int64
	// Fetch downloads finished videos, HTTP when nil. A download may be
	// redirected to a link OpenRouter signed, so framy points this at the
	// client that refuses private addresses (safehttp.Client). The key goes
	// only to the host it was first sent to.
	Fetch *http.Client
	// Callbacks puts a callback_url on every video request. The zero value
	// sends none, and the poll alone finishes the run.
	Callbacks Callbacks
}

// Provider is the OpenRouter adapter of the job queue.
type Provider struct {
	cfg Config
}

// New builds the adapter.
func New(cfg Config) *Provider {
	cfg.Base = strings.TrimRight(cfg.Base, "/")
	if cfg.Base == "" {
		cfg.Base = DefaultBase
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = DefaultMaxResponse
	}
	if cfg.MaxVideo <= 0 {
		cfg.MaxVideo = DefaultMaxVideo
	}
	return &Provider{cfg: cfg}
}

// Submit runs an image, or sends a video and returns its job id. Everything
// framy can check is checked before the request goes, so a run refused for
// its settings costs nothing.
func (p *Provider) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	if j.Kind == catalog.Video {
		return p.submitVideo(ctx, j)
	}
	if j.Kind != catalog.Image {
		return jobs.Submission{}, &jobs.Error{Class: jobs.NoAdapter, Err: fmt.Errorf("framy cannot run OpenRouter %s", j.Kind)}
	}
	body, err := imageRequest(j)
	if err != nil {
		return jobs.Submission{}, err
	}
	key, err := p.key(ctx, j)
	if err != nil {
		return jobs.Submission{}, err
	}
	out, err := p.images(ctx, key, body)
	if err != nil {
		return jobs.Submission{}, err
	}
	return jobs.Submission{Outcome: &out}, nil
}

// Poll asks about a video. It is never called for an image: Submit returns
// the result.
func (p *Provider) Poll(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	if j.Kind == catalog.Video {
		return p.pollVideo(ctx, j)
	}
	return jobs.Outcome{}, &jobs.Error{Class: jobs.NoAdapter, Err: fmt.Errorf("OpenRouter %s runs are not polled yet", j.Kind)}
}

func (p *Provider) key(ctx context.Context, j jobs.Job) (string, error) {
	if p.cfg.Key == nil {
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenRouter key is configured")}
	}
	key, err := p.cfg.Key(ctx, j)
	var pe *jobs.Error
	switch {
	case errors.As(err, &pe):
		return "", pe
	case err != nil:
		return "", &jobs.Error{Class: jobs.Internal, Err: fmt.Errorf("find the key: %w", err)}
	case key == "":
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenRouter key pays for this job")}
	}
	return key, nil
}

// imageResponse is the answer of POST /images. An error can come with a 200
// when it happened after the provider took the request.
type imageResponse struct {
	Data []struct {
		B64JSON   string `json:"b64_json"`
		MediaType string `json:"media_type"`
	} `json:"data"`
	Usage *struct {
		Cost *float64 `json:"cost"`
	} `json:"usage"`
	Error *errorBody `json:"error"`
}

func (p *Provider) images(ctx context.Context, key string, body map[string]any) (jobs.Outcome, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Base+"/images", bytes.NewReader(raw))
	if err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
	if p.cfg.Referer != "" {
		req.Header.Set("HTTP-Referer", p.cfg.Referer)
	}
	if p.cfg.Title != "" {
		req.Header.Set("X-Title", p.cfg.Title)
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return jobs.Outcome{}, transportError(err)
	}
	defer resp.Body.Close()

	limited := http.MaxBytesReader(nil, resp.Body, p.cfg.MaxResponse)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(limited, 1<<20))
		return jobs.Outcome{}, statusError(resp.StatusCode, resp.Header, data)
	}
	var r imageResponse
	if err := json.NewDecoder(limited).Decode(&r); err != nil {
		if ctx.Err() != nil {
			// Cut short by the deadline or a stop: the queue reads which.
			return jobs.Outcome{}, fmt.Errorf("read the answer of openrouter: %w", err)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = fmt.Errorf("the answer is larger than %d MiB", p.cfg.MaxResponse>>20)
		}
		// The run was likely paid, so this is not tried again.
		return jobs.Outcome{}, &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of openrouter: %w", err)}
	}
	var out jobs.Outcome
	if r.Usage != nil {
		out.CostUSD = r.Usage.Cost
	}
	if r.Error != nil {
		out.Err = bodyError(r.Error)
		return out, nil
	}
	for i, d := range r.Data {
		res, err := result(d.B64JSON, d.MediaType)
		if err != nil {
			out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("image %d of the answer: %w", i+1, err)}
			return out, nil
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// result decodes one image of the answer.
func result(b64, mediaType string) (jobs.Result, error) {
	if rest, ok := strings.CutPrefix(b64, "data:"); ok {
		// A data URL instead of bare base64.
		meta, data, found := strings.Cut(rest, ",")
		if !found {
			return jobs.Result{}, errors.New("a data URL with no data")
		}
		if mediaType == "" {
			mediaType, _, _ = strings.Cut(meta, ";")
		}
		b64 = data
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return jobs.Result{}, fmt.Errorf("decode base64: %w", err)
	}
	if len(data) == 0 {
		return jobs.Result{}, errors.New("an empty image")
	}
	if mediaType == "" {
		mediaType = sniff(data)
	}
	return jobs.Result{ContentType: mediaType, Data: data}, nil
}

// sniff names an image the answer did not name.
func sniff(data []byte) string {
	ct := http.DetectContentType(data)
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	if bytes.Contains(data[:min(len(data), 1024)], []byte("<svg")) {
		return "image/svg+xml"
	}
	return ct
}

// transportError reads a failed call. A connection that was never made sent
// nothing, so it is safe to try again; anything later may have been paid.
func transportError(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("reach openrouter: %w", err)}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("reach openrouter: %w", err)}
	}
	// A deadline or a stop: the queue reads the context.
	return fmt.Errorf("call openrouter: %w", err)
}
