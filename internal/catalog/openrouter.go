// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultOpenRouterBase is OpenRouter's API. Its catalogs need no key.
const DefaultOpenRouterBase = "https://openrouter.ai/api/v1"

// maxBody bounds one catalog response; the largest is about 100 KB.
const maxBody = 8 << 20

// OpenRouterSource reads OpenRouter's public catalogs:
//
//   - /images/models, and /images/models/{id}/endpoints for each image model,
//     which holds the exact price of every upstream route;
//   - /videos/models, with the parameters and pricing_skus of every video model;
//   - /models, the only one that carries expiration_date.
type OpenRouterSource struct {
	Base      string
	HTTP      *http.Client
	UserAgent string
	// Parallel bounds the endpoint requests in flight.
	Parallel int
}

// Upstream is what one fetch saw.
type Upstream struct {
	Offers []Offer
	// Partial lists image offers whose endpoints could not be read; the
	// store keeps what it had for them.
	Partial []string
}

type orListing struct {
	Data []json.RawMessage `json:"data"`
}

type orImageModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Architecture struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	SupportedParameters map[string]Param `json:"supported_parameters"`
}

type orEndpoints struct {
	Endpoints []struct {
		ProviderSlug        string           `json:"provider_slug"`
		SupportedParameters map[string]Param `json:"supported_parameters"`
		Passthrough         []string         `json:"allowed_passthrough_parameters"`
		Pricing             []Price          `json:"pricing"`
	} `json:"endpoints"`
}

type orVideoModel struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	Resolutions          []string          `json:"supported_resolutions"`
	AspectRatios         []string          `json:"supported_aspect_ratios"`
	Sizes                []string          `json:"supported_sizes"`
	Durations            []float64         `json:"supported_durations"`
	FrameImages          []string          `json:"supported_frame_images"`
	UpscaleFactor        *orRange          `json:"upscale_factor"`
	Creativity           []float64         `json:"creativity"`
	GenerateAudio        *bool             `json:"generate_audio"`
	Seed                 *bool             `json:"seed"`
	PricingSKUs          map[string]string `json:"pricing_skus"`
	PassthroughParameter []string          `json:"allowed_passthrough_parameters"`
}

type orRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type orModel struct {
	ID           string `json:"id"`
	Architecture struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	ExpirationDate *string `json:"expiration_date"`
}

// Fetch reads every catalog. A listing that fails fails the fetch, so a bad
// hour changes nothing; an image model whose endpoints fail is left out and
// named in Partial, unless so many fail that the answer cannot be trusted.
func (s *OpenRouterSource) Fetch(ctx context.Context) (Upstream, error) {
	var models []orModel
	if err := s.list(ctx, "/models?output_modalities=image,video", &models); err != nil {
		return Upstream{}, err
	}
	meta := make(map[string]orModel, len(models))
	for _, m := range models {
		meta[m.ID] = m
	}
	var images []orImageModel
	if err := s.list(ctx, "/images/models", &images); err != nil {
		return Upstream{}, err
	}
	var videos []orVideoModel
	if err := s.list(ctx, "/videos/models", &videos); err != nil {
		return Upstream{}, err
	}
	if len(images) == 0 || len(videos) == 0 {
		// An empty list is an outage, not every model leaving at once.
		return Upstream{}, fmt.Errorf("openrouter catalog: %d image and %d video models listed", len(images), len(videos))
	}

	routes, failed := s.endpoints(ctx, images)
	if len(failed)*2 > len(images) {
		return Upstream{}, fmt.Errorf("openrouter catalog: endpoints failed for %d of %d image models", len(failed), len(images))
	}
	var up Upstream
	for _, m := range images {
		id := OfferID(OpenRouter, m.ID)
		if _, bad := failed[m.ID]; bad {
			up.Partial = append(up.Partial, id)
			continue
		}
		up.Offers = append(up.Offers, Offer{
			ID:              id,
			Provider:        OpenRouter,
			ProviderModelID: m.ID,
			Kind:            Image,
			Name:            m.Name,
			Description:     m.Description,
			InputModalities: m.Architecture.InputModalities,
			Params:          m.SupportedParameters,
			Routes:          routes[m.ID],
			ExpiresOn:       expiration(meta[m.ID].ExpirationDate),
		})
	}
	for _, m := range videos {
		up.Offers = append(up.Offers, Offer{
			ID:              OfferID(OpenRouter, m.ID),
			Provider:        OpenRouter,
			ProviderModelID: m.ID,
			Kind:            Video,
			Name:            m.Name,
			Description:     m.Description,
			InputModalities: meta[m.ID].Architecture.InputModalities,
			Params:          m.params(),
			Routes: []Route{{
				Passthrough: m.PassthroughParameter,
				SKUs:        m.PricingSKUs,
			}},
			ExpiresOn: expiration(meta[m.ID].ExpirationDate),
		})
	}
	return up, nil
}

// params turns a video model's fields into the same enum, range and boolean
// form the image models use. A flag that is null says nothing, so only a true
// one becomes a toggle.
func (m orVideoModel) params() map[string]Param {
	out := map[string]Param{}
	enum := func(name string, values []string) {
		if len(values) > 0 {
			out[name] = Param{Type: Enum, Values: anys(values)}
		}
	}
	enum("resolution", m.Resolutions)
	enum("aspect_ratio", m.AspectRatios)
	enum("size", m.Sizes)
	enum("frame_images", m.FrameImages)
	if len(m.Durations) > 0 {
		durations := slices.Clone(m.Durations)
		slices.Sort(durations)
		out["duration"] = Param{Type: Enum, Values: anys(durations)}
	}
	if r := m.UpscaleFactor; r != nil {
		out["upscale_factor"] = Param{Type: Range, Min: &r.Min, Max: &r.Max}
	}
	if len(m.Creativity) == 2 {
		out["creativity"] = Param{Type: Range, Min: &m.Creativity[0], Max: &m.Creativity[1]}
	}
	if m.GenerateAudio != nil && *m.GenerateAudio {
		out["generate_audio"] = Param{Type: Boolean}
	}
	if m.Seed != nil && *m.Seed {
		out["seed"] = Param{Type: Boolean}
	}
	return out
}

func anys[T any](values []T) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func expiration(date *string) *time.Time {
	if date == nil || *date == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, (*date)[:min(len(*date), len(time.DateOnly))])
	if err != nil {
		return nil
	}
	return &t
}

// endpoints reads the routes of every image model, a few at a time.
func (s *OpenRouterSource) endpoints(ctx context.Context, images []orImageModel) (map[string][]Route, map[string]error) {
	parallel := max(s.Parallel, 1)
	var (
		mu     sync.Mutex
		routes = map[string][]Route{}
		failed = map[string]error{}
		wg     sync.WaitGroup
		slots  = make(chan struct{}, parallel)
	)
	for _, m := range images {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			var e orEndpoints
			// An empty list is an answer: the model is listed but cannot run
			// on the Images API, as meta/muse-image. Build hides it.
			err := s.get(ctx, "/images/models/"+escapeID(m.ID)+"/endpoints", &e)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed[m.ID] = err
				return
			}
			for _, ep := range e.Endpoints {
				routes[m.ID] = append(routes[m.ID], Route{
					Provider:    ep.ProviderSlug,
					Params:      ep.SupportedParameters,
					Passthrough: ep.Passthrough,
					Prices:      ep.Pricing,
				})
			}
		})
	}
	wg.Wait()
	return routes, failed
}

// escapeID escapes each segment of "author/slug" and keeps the slash, which
// is part of the path.
func escapeID(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// list reads a {"data": [...]} listing. An entry that does not decode is an
// error: half a catalog would disable the other half.
func (s *OpenRouterSource) list(ctx context.Context, path string, out any) error {
	var l orListing
	if err := s.get(ctx, path, &l); err != nil {
		return err
	}
	raw, err := json.Marshal(l.Data)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("openrouter %s: %w", path, err)
	}
	return nil
}

func (s *OpenRouterSource) get(ctx context.Context, path string, out any) error {
	base := strings.TrimRight(s.Base, "/")
	if base == "" {
		base = DefaultOpenRouterBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("openrouter %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return fmt.Errorf("openrouter %s: %w", path, err)
	}
	return nil
}
