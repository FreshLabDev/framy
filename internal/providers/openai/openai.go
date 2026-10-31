// SPDX-License-Identifier: Apache-2.0

// Package openai runs image generations on OpenAI's own API, with a person's
// key. The Images API answers with the image itself, so a run is one call:
// POST /v1/images/generations for a prompt, POST /v1/images/edits when the
// run takes reference images (docs/design.md, Jobs). What only the direct API
// has is here: any size up to 3840 px, and quality up to max. OpenAI has no
// video any more.
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// DefaultBase is OpenAI's API.
const DefaultBase = "https://api.openai.com/v1"

// DefaultMaxResponse bounds an answer: a 4K PNG is about 30 MB in base64,
// and a run may return up to ten.
const DefaultMaxResponse = 256 << 20

// Config is how the adapter reaches OpenAI.
type Config struct {
	// Base is the API root, DefaultBase when empty.
	Base string
	// HTTP makes the calls. The queue's context bounds each one, so the
	// client needs no timeout of its own.
	HTTP      *http.Client
	UserAgent string
	// Key returns the key that pays for the job: the person's connection.
	// An *jobs.Error keeps its class; any other error fails the job as
	// internal. The key never reaches a log or an error.
	Key func(ctx context.Context, j jobs.Job) (string, error)
	// MaxResponse bounds an answer, DefaultMaxResponse when zero.
	MaxResponse int64
}

// Provider is the OpenAI adapter of the job queue.
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
	return &Provider{cfg: cfg}
}

// Submit runs an image. Everything framy can check is checked before the key
// is asked for and the request goes, so a run refused for its settings costs
// nothing.
func (p *Provider) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	if j.Kind != catalog.Image {
		return jobs.Submission{}, &jobs.Error{Class: jobs.NoAdapter, Err: errors.New("OpenAI has no video; the Videos API closed on 24 September 2026")}
	}
	req, err := imageRequest(j)
	if err != nil {
		return jobs.Submission{}, err
	}
	key, err := p.key(ctx, j)
	if err != nil {
		return jobs.Submission{}, err
	}
	out, err := p.call(ctx, key, req)
	if err != nil {
		return jobs.Submission{}, err
	}
	return jobs.Submission{Outcome: &out}, nil
}

// Poll is never called for an image: Submit returns the result.
func (p *Provider) Poll(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	return jobs.Outcome{}, &jobs.Error{Class: jobs.NoAdapter, Err: fmt.Errorf("OpenAI %s runs are not polled", j.Kind)}
}

func (p *Provider) key(ctx context.Context, j jobs.Job) (string, error) {
	if p.cfg.Key == nil {
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenAI key is configured")}
	}
	key, err := p.cfg.Key(ctx, j)
	var pe *jobs.Error
	switch {
	case errors.As(err, &pe):
		return "", pe
	case err != nil:
		return "", &jobs.Error{Class: jobs.Internal, Err: fmt.Errorf("find the key: %w", err)}
	case key == "":
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenAI key pays for this job")}
	}
	return key, nil
}

// imagesResponse is the answer of both endpoints. An error can come with a
// 200 when it happened after OpenAI took the request.
type imagesResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
	// OutputFormat is png, jpeg or webp.
	OutputFormat string     `json:"output_format"`
	Usage        *usage     `json:"usage"`
	Error        *errorBody `json:"error"`
}

func (p *Provider) call(ctx context.Context, key string, r request) (jobs.Outcome, error) {
	raw, err := json.Marshal(r.body)
	if err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Base+r.path, bytes.NewReader(raw))
	if err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+key)
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
	var a imagesResponse
	if err := json.NewDecoder(limited).Decode(&a); err != nil {
		if ctx.Err() != nil {
			// Cut short by the deadline or a stop: the queue reads which.
			return jobs.Outcome{}, fmt.Errorf("read the answer of openai: %w", err)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = fmt.Errorf("the answer is larger than %d MiB", p.cfg.MaxResponse>>20)
		}
		// The run was likely paid, so this is not tried again.
		return jobs.Outcome{}, &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of openai: %w", err)}
	}
	var out jobs.Outcome
	if a.Usage != nil {
		out.CostUSD = r.rates.cost(*a.Usage)
	}
	if a.Error != nil {
		out.Err = bodyError(a.Error, key)
		return out, nil
	}
	for i, d := range a.Data {
		res, err := result(d.B64JSON, a.OutputFormat, r.body)
		if err != nil {
			out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("image %d of the answer: %w", i+1, err)}
			return out, nil
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// result decodes one image of the answer. The bytes say what they are; the
// answer's output_format, then the one asked for, name the type when they
// do not.
func result(b64, format string, asked map[string]any) (jobs.Result, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return jobs.Result{}, fmt.Errorf("decode base64: %w", err)
	}
	if len(data) == 0 {
		return jobs.Result{}, errors.New("an empty image")
	}
	if ct := http.DetectContentType(data); strings.HasPrefix(ct, "image/") {
		return jobs.Result{ContentType: ct, Data: data}, nil
	}
	if format == "" {
		format, _ = asked["output_format"].(string)
	}
	if format == "" {
		format = "png"
	}
	return jobs.Result{ContentType: "image/" + format, Data: data}, nil
}

// rates are what OpenAI bills per token, in dollars, from the offer's price
// lines.
type rates struct {
	text, image, output float64
}

// usage is the usage object of an answer. The input tokens are text and
// image tokens (the references), the output tokens are the image.
type usage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		TextTokens  int64 `json:"text_tokens"`
		ImageTokens int64 `json:"image_tokens"`
	} `json:"input_tokens_details"`
}

// ratesOf reads the rates from the offer's route. The lines are the catalog's
// own, the ones the estimate is made from, so the price after the run and the
// price before it come from the same rates. input_image is needed only by a
// run that has references.
func ratesOf(of catalog.Offer, withReferences bool) (rates, error) {
	if len(of.Routes) == 0 {
		return rates{}, refused("the catalog lists no route for %s", of.ID)
	}
	var r rates
	var seen struct{ text, image, output bool }
	for _, p := range of.Routes[0].Prices {
		if p.Unit != "token" || p.Variant != "" {
			continue
		}
		switch p.Billable {
		case "input_text":
			r.text, seen.text = p.CostUSD, true
		case "input_image":
			r.image, seen.image = p.CostUSD, true
		case "output_image":
			r.output, seen.output = p.CostUSD, true
		}
	}
	switch {
	case !seen.text:
		return rates{}, refused("%s has no price per input text token in the catalog", of.ID)
	case !seen.output:
		return rates{}, refused("%s has no price per output image token in the catalog", of.ID)
	case withReferences && !seen.image:
		return rates{}, refused("%s has no price per input image token in the catalog", of.ID)
	}
	return r, nil
}

// cost is the run's price: the tokens times the rates. Input tokens the
// details do not account for, or all of them when the answer has no details,
// count at the dearer input rate, so the cost is never below what OpenAI
// billed. It is rounded to a nano-dollar, which hides only binary noise.
func (r rates) cost(u usage) *float64 {
	var text, image int64
	if d := u.InputTokensDetails; d != nil {
		text, image = d.TextTokens, d.ImageTokens
	}
	usd := float64(text)*r.text + float64(image)*r.image + float64(u.OutputTokens)*r.output
	if rest := u.InputTokens - text - image; rest > 0 {
		usd += float64(rest) * math.Max(r.text, r.image)
	}
	usd = math.Round(usd*1e9) / 1e9
	return &usd
}

// transportError reads a failed call. A connection that was never made sent
// nothing, so it is safe to try again; anything later may have been paid.
func transportError(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("reach openai: %w", err)}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return &jobs.Error{Class: jobs.Unavailable, Err: fmt.Errorf("reach openai: %w", err)}
	}
	// A deadline or a stop: the queue reads the context.
	return fmt.Errorf("call openai: %w", err)
}
