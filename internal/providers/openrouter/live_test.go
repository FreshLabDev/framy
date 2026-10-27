// SPDX-License-Identifier: Apache-2.0
//go:build live

// The live tests run on OpenRouter itself and spend real credit, about
// $0.05 a run: two images on the free model and one prompt its moderation
// refuses. The key is read from a file and goes nowhere but the
// Authorization header:
//
//	OPENROUTER_TEST_KEY_FILE=/path/to/key go test -tags=live -run Live -v ./internal/providers/openrouter/
//
// OPENROUTER_TEST_OUT, when set, is a directory the images are written to.
package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	imagepng "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

func liveKey(t *testing.T) string {
	t.Helper()
	path := os.Getenv("OPENROUTER_TEST_KEY_FILE")
	if path == "" {
		t.Skip("set OPENROUTER_TEST_KEY_FILE to run the live tests; they spend real credit")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the key file: %v", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		t.Fatal("the key file is empty")
	}
	return key
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// liveOffer reads a model's routes as the catalog sync does.
func liveOffer(t *testing.T, model string) catalog.Offer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultBase+"/images/models/"+model+"/endpoints", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct {
		Endpoints []struct {
			ProviderSlug        string                   `json:"provider_slug"`
			SupportedParameters map[string]catalog.Param `json:"supported_parameters"`
			Passthrough         []string                 `json:"allowed_passthrough_parameters"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("endpoints of %s: %d %v", model, resp.StatusCode, err)
	}
	of := catalog.Offer{
		ID: catalog.OfferID(catalog.OpenRouter, model), ModelID: model, Provider: catalog.OpenRouter,
		ProviderModelID: model, Kind: catalog.Image, Params: map[string]catalog.Param{},
	}
	for _, ep := range e.Endpoints {
		of.Routes = append(of.Routes, catalog.Route{Provider: ep.ProviderSlug, Params: ep.SupportedParameters, Passthrough: ep.Passthrough})
		for name, p := range ep.SupportedParameters {
			of.Params[name] = p
		}
	}
	if len(of.Routes) == 0 {
		t.Fatalf("%s has no route on the Images API", model)
	}
	return of
}

func liveJob(of catalog.Offer, prompt string, inputs ...jobs.Input) jobs.Job {
	return jobs.Job{
		ID: 1, UserID: 1, OfferID: of.ID, ModelID: of.ModelID, Provider: of.Provider, Kind: catalog.Image,
		Prompt: prompt, Settings: map[string]any{}, Free: true, Offer: of, Inputs: inputs,
	}
}

func liveProvider(key string) *Provider {
	return New(Config{
		UserAgent: "framy-live-test (+https://github.com/FreshLabDev/framy)",
		Referer:   "https://github.com/FreshLabDev/framy",
		Title:     "framy",
		Key:       func(context.Context, jobs.Job) (string, error) { return key, nil },
	})
}

func run(t *testing.T, p *Provider, j jobs.Job) (jobs.Outcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sub, err := p.Submit(ctx, j)
	if err != nil {
		return jobs.Outcome{}, err
	}
	return *sub.Outcome, nil
}

func save(t *testing.T, name string, r jobs.Result) {
	t.Helper()
	dir := os.Getenv("OPENROUTER_TEST_OUT")
	if dir == "" {
		return
	}
	ext := strings.TrimPrefix(r.ContentType, "image/")
	path := filepath.Join(dir, name+"."+ext)
	if err := os.WriteFile(path, r.Data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

func dataURL(r jobs.Result) string {
	return "data:" + r.ContentType + ";base64," + base64.StdEncoding.EncodeToString(r.Data)
}

// backdrop is a second reference framy draws itself: a blue gradient.
func backdrop(t *testing.T) jobs.Result {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := range 512 {
		for x := range 512 {
			img.Set(x, y, color.RGBA{R: 20, G: uint8(60 + y/8), B: uint8(140 + x/5), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := imagepng.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return jobs.Result{ContentType: "image/png", Data: buf.Bytes()}
}

func cost(out jobs.Outcome) string {
	if out.CostUSD == nil {
		return "none"
	}
	return fmt.Sprintf("$%.6f", *out.CostUSD)
}

func TestLiveGenerationAndEdit(t *testing.T) {
	key := liveKey(t)
	p := liveProvider(key)
	of := liveOffer(t, env("OPENROUTER_TEST_MODEL", "microsoft/mai-image-2.6-flash"))

	out, err := run(t, p, liveJob(of, "A red apple on a wooden table, soft daylight, photo"))
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if out.Err != nil || len(out.Results) == 0 {
		t.Fatalf("generation: %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 {
		t.Errorf("generation cost %s: the actual price must come back", cost(out))
	}
	t.Logf("generation: %d image(s), %s, %d bytes, cost %s", len(out.Results), out.Results[0].ContentType, len(out.Results[0].Data), cost(out))
	save(t, "generation", out.Results[0])

	apple := out.Results[0]
	out, err = run(t, p, liveJob(of, "Put the apple from the first image on the blue background of the second image",
		jobs.Input{ContentType: apple.ContentType, URL: dataURL(apple)},
		jobs.Input{ContentType: "image/png", URL: dataURL(backdrop(t))},
	))
	if err != nil {
		t.Fatalf("edit with two references: %v", err)
	}
	if out.Err != nil || len(out.Results) == 0 {
		t.Fatalf("edit with two references: %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 {
		t.Errorf("edit cost %s", cost(out))
	}
	t.Logf("edit: %d image(s), cost %s", len(out.Results), cost(out))
	save(t, "edit", out.Results[0])
}

func TestLiveModerationIsARefusal(t *testing.T) {
	key := liveKey(t)
	p := liveProvider(key)
	// OpenRouter moderates the input of OpenAI's models before the run.
	of := liveOffer(t, env("OPENROUTER_TEST_MODERATED_MODEL", "openai/gpt-image-1-mini"))
	j := liveJob(of, "An explicit pornographic photo of two nude adults having sex")
	if q, ok := of.Params["quality"]; ok && q.Type == catalog.Enum && accepts("quality", q, "low") {
		j.Settings["quality"] = "low"
	}
	out, err := run(t, p, j)
	pe := (*jobs.Error)(nil)
	switch {
	case errors.As(err, &pe):
	case err != nil:
		t.Fatalf("a failed call, not a refusal: %v", err)
	case out.Err != nil:
		pe = out.Err
	default:
		t.Fatalf("the prompt was not refused: %d image(s), cost %s", len(out.Results), cost(out))
	}
	if pe.Class != jobs.Moderation {
		t.Fatalf("class %s (%v), want moderation", pe.Class, pe)
	}
	t.Logf("refused: %v", pe)
}
