// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/providers/answer"
)

// freeRoute is the route whose free variant takes the job, by the rule the
// estimate used (price.FreeRoute): a run the estimate showed as free goes
// there, and no other.
func freeRoute(j jobs.Job, now time.Time) (catalog.Route, bool) {
	req := price.Request{Settings: j.Settings, Now: now}
	for _, in := range j.Inputs {
		if strings.HasPrefix(in.ContentType, "image/") {
			req.Images = append(req.Images, price.Image{})
		} else {
			// A video or a sound: no free variant takes one.
			return catalog.Route{}, false
		}
	}
	return price.FreeRoute(j.Offer, req)
}

// freeConfig are the settings a free variant's request carries, under
// image_config. n is always 1 there, since one request makes one image.
var freeConfig = map[string]string{"aspect_ratio": "aspect_ratio", "resolution": "image_size"}

// freeRequest builds the body of POST /chat/completions for a free variant.
// The Images API does not list ":free" ids, and chat completions answers
// with the image in the message (docs/research/providers.md). A setting the
// variant has no field for refuses the run before anything goes, as the
// Images API's routes do.
func freeRequest(j jobs.Job, r catalog.Route) (map[string]any, error) {
	if strings.TrimSpace(j.Prompt) == "" {
		return nil, refused("the prompt is empty")
	}
	config := map[string]any{}
	for name, v := range j.Settings {
		if v == nil {
			continue
		}
		if name == "n" {
			continue
		}
		field, ok := freeConfig[name]
		if !ok {
			return nil, refused("%s takes no %s", r.Free.Model, name)
		}
		if s, isText := v.(string); isText && strings.EqualFold(s, "auto") {
			continue
		}
		config[field] = v
	}
	content := []map[string]any{{"type": "text", "text": j.Prompt}}
	for i, in := range j.Inputs {
		if in.URL == "" {
			return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no link", i+1)}
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": in.URL}})
	}
	body := map[string]any{
		"model":      r.Free.Model,
		"messages":   []map[string]any{{"role": "user", "content": content}},
		"modalities": []string{"image"},
		"provider":   map[string]any{"only": []string{r.Provider}},
		"user":       jobs.SafetyID(j.UserID),
	}
	if len(config) > 0 {
		body["image_config"] = config
	}
	return body, nil
}

// chatResponse is the answer of POST /chat/completions with images.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Images []struct {
				ImageURL struct {
					URL answer.Base64 `json:"url"`
				} `json:"image_url"`
			} `json:"images"`
		} `json:"message"`
		Error *errorBody `json:"error"`
	} `json:"choices"`
	Usage *usage     `json:"usage"`
	Error *errorBody `json:"error"`
}

// freeImages runs a free variant. A free model's limits are OpenRouter's
// per account (20 a minute, and 50 or 1,000 a day), and a 429 for them
// comes back as RateLimited with the wait OpenRouter gave.
func (p *Provider) freeImages(ctx context.Context, key string, body map[string]any) (jobs.Outcome, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req, err := p.newRequest(ctx, key, http.MethodPost, p.cfg.Base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return jobs.Outcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
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
	var r chatResponse
	if err := json.NewDecoder(limited).Decode(&r); err != nil {
		if ctx.Err() != nil {
			return jobs.Outcome{}, fmt.Errorf("read the answer of openrouter: %w", err)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = fmt.Errorf("the answer is larger than %d MiB", p.cfg.MaxResponse>>20)
		}
		return jobs.Outcome{}, &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of openrouter: %w", err)}
	}
	zero := 0.0
	out := jobs.Outcome{CostUSD: r.Usage.total()}
	if out.CostUSD == nil {
		// A free variant has no price: a usage block left out is no cost.
		out.CostUSD = &zero
	}
	if r.Error != nil {
		out.Err = bodyError(r.Error)
		return out, nil
	}
	n := 0
	for _, c := range r.Choices {
		if c.Error != nil && out.Err == nil {
			out.Err = bodyError(c.Error)
		}
		for _, im := range c.Message.Images {
			n++
			res, err := result(im.ImageURL.URL, "")
			if err != nil {
				if out.Lost == nil {
					out.Lost = fmt.Errorf("image %d of the answer: %w", n, err)
				}
				continue
			}
			out.Results = append(out.Results, res)
		}
	}
	switch {
	case len(out.Results) > 0:
		out.Err = nil
	case out.Err != nil:
	case out.Lost != nil:
		out.Err, out.Lost = &jobs.Error{Class: jobs.ProviderFailed, Err: out.Lost}, nil
	default:
		out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("the answer has no image")}
	}
	return out, nil
}
