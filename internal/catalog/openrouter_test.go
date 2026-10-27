// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The fixtures are OpenRouter's answers of 27 October 2026, descriptions cut
// short: the three listings in full, and the endpoints of the image models
// the tests look at closely. Any other model's endpoints are made up from
// its listing, with a placeholder price.
const fixtures = "testdata/openrouter"

// fixtureServer serves the fixtures; override replaces a path's answer.
func fixtureServer(t *testing.T, override map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	listing := map[string]string{
		"/api/v1/models":        "models.json",
		"/api/v1/images/models": "images.json",
		"/api/v1/videos/models": "videos.json",
	}
	var images struct {
		Data []struct {
			ID                  string           `json:"id"`
			SupportedParameters map[string]Param `json:"supported_parameters"`
		} `json:"data"`
	}
	readJSON(t, filepath.Join(fixtures, "images.json"), &images)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := override[r.URL.Path]; ok {
			h(w, r)
			return
		}
		if name, ok := listing[r.URL.Path]; ok {
			http.ServeFile(w, r, filepath.Join(fixtures, name))
			return
		}
		id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/images/models/"), "/endpoints")
		if !ok {
			http.NotFound(w, r)
			return
		}
		file := filepath.Join(fixtures, "endpoints", strings.ReplaceAll(id, "/", "_")+".json")
		if _, err := os.Stat(file); err == nil {
			http.ServeFile(w, r, file)
			return
		}
		for _, m := range images.Data {
			if m.ID == id {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "endpoints": []any{map[string]any{
					"provider_slug":                  "fixture",
					"supported_parameters":           m.SupportedParameters,
					"allowed_passthrough_parameters": []string{},
					"pricing":                        []Price{{Billable: "output_image", Unit: "image", CostUSD: 0.01}},
				}}})
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func source(srv *httptest.Server) *OpenRouterSource {
	return &OpenRouterSource{Base: srv.URL + "/api/v1", HTTP: srv.Client(), Parallel: 4}
}

func offerByID(t *testing.T, offers []Offer, id string) Offer {
	t.Helper()
	for _, of := range offers {
		if of.ID == id {
			return of
		}
	}
	t.Fatalf("offer %s missing", id)
	return Offer{}
}

func TestFetchReadsEveryCatalog(t *testing.T) {
	up, err := source(fixtureServer(t, nil)).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Partial) != 0 {
		t.Fatalf("partial = %v", up.Partial)
	}
	kinds := map[Kind]int{}
	for _, of := range up.Offers {
		kinds[of.Kind]++
	}
	if kinds[Image] != 61 || kinds[Video] != 30 {
		t.Fatalf("offers by kind = %v, want 61 images and 30 videos", kinds)
	}

	nb := offerByID(t, up.Offers, "openrouter:google/gemini-nano-banana-2.1")
	if nb.Name != "Google: Nano Banana 2.1" || nb.ModelID != "" || len(nb.Routes) != 1 {
		t.Fatalf("nano banana = %+v", nb)
	}
	r := nb.Routes[0]
	if r.Provider != "google-ai-studio" || !r.accepts("cachedContent") || len(r.Prices) != 1 || r.Prices[0].CostUSD != 3e-05 {
		t.Fatalf("nano banana route = %+v", r)
	}
	flash := offerByID(t, up.Offers, "openrouter:google/gemini-3.1-flash-image")
	if len(flash.Routes) != 3 {
		t.Fatalf("3.1 flash has %d routes, want one per upstream", len(flash.Routes))
	}

	seedance := offerByID(t, up.Offers, "openrouter:bytedance/seedance-1-5-pro")
	if seedance.ExpiresOn == nil || seedance.ExpiresOn.Format(time.DateOnly) != "2026-11-11" {
		t.Fatalf("seedance 1.5 expires %v, want 2026-11-11 from /models", seedance.ExpiresOn)
	}
	veo := offerByID(t, up.Offers, "openrouter:google/veo-3.1-lite")
	if got := veo.Params["duration"].Values; !sameValue(got, []any{4, 6, 8}) {
		t.Fatalf("veo lite durations = %v, want sorted 4 6 8", got)
	}
	if veo.Params["generate_audio"].Type != Boolean || veo.Params["seed"].Type != Boolean {
		t.Fatalf("veo lite params = %v", veo.Params)
	}
	if !slices.Equal(veo.InputModalities, []string{"text", "image"}) {
		t.Fatalf("veo lite input modalities = %v", veo.InputModalities)
	}
	if !veo.Routes[0].accepts("negativePrompt") || veo.Routes[0].SKUs["duration_seconds_with_audio"] == "" {
		t.Fatalf("veo lite route = %+v", veo.Routes[0])
	}
	grok := offerByID(t, up.Offers, "openrouter:x-ai/grok-imagine-video-1.5-lite")
	if _, ok := grok.Params["generate_audio"]; ok {
		t.Fatal("a null generate_audio became a toggle")
	}
	upscale := offerByID(t, up.Offers, "openrouter:black-forest-labs/flux-video-upscale")
	if p := upscale.Params["upscale_factor"]; p.Type != Range || *p.Min != 1.5 || *p.Max != 3 {
		t.Fatalf("upscale factor = %+v", p)
	}
	muse := offerByID(t, up.Offers, "openrouter:meta/muse-image")
	if len(muse.Routes) != 0 {
		t.Fatalf("muse has routes %v; an empty endpoint list is an answer", muse.Routes)
	}
}

func TestFetchChangesNothingWhenAListingFails(t *testing.T) {
	for _, path := range []string{"/api/v1/models", "/api/v1/images/models", "/api/v1/videos/models"} {
		t.Run(path, func(t *testing.T) {
			srv := fixtureServer(t, map[string]http.HandlerFunc{path: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream", http.StatusBadGateway)
			}})
			if _, err := source(srv).Fetch(context.Background()); err == nil {
				t.Fatal("a failed listing passed")
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		srv := fixtureServer(t, map[string]http.HandlerFunc{"/api/v1/videos/models": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`))
		}})
		if _, err := source(srv).Fetch(context.Background()); err == nil {
			t.Fatal("an empty listing passed, which would disable every video model")
		}
	})
}

func TestFetchKeepsAModelWhoseEndpointsFail(t *testing.T) {
	srv := fixtureServer(t, map[string]http.HandlerFunc{
		"/api/v1/images/models/krea/k3-edit/endpoints": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "busy", http.StatusTooManyRequests)
		},
	})
	up, err := source(srv).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(up.Partial, []string{"openrouter:krea/k3-edit"}) {
		t.Fatalf("partial = %v", up.Partial)
	}
	for _, of := range up.Offers {
		if of.ID == "openrouter:krea/k3-edit" {
			t.Fatal("an offer without fresh endpoints was passed on")
		}
	}
}

func TestFetchFailsWhenMostEndpointsFail(t *testing.T) {
	var calls atomic.Int32
	srv := fixtureServer(t, nil)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/endpoints") {
			calls.Add(1)
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		resp, err := srv.Client().Get(srv.URL + r.URL.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		var body json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_, _ = w.Write(body)
	}))
	t.Cleanup(failing.Close)
	_, err := (&OpenRouterSource{Base: failing.URL + "/api/v1", Parallel: 8}).Fetch(context.Background())
	if err == nil || calls.Load() == 0 {
		t.Fatalf("err = %v after %d endpoint calls", err, calls.Load())
	}
}

func TestEscapeIDKeepsTheSlash(t *testing.T) {
	if got := escapeID("black-forest-labs/flux.2-pro"); got != "black-forest-labs/flux.2-pro" {
		t.Fatal(got)
	}
	if got := escapeID("a b/c?d"); got != "a%20b/c%3Fd" {
		t.Fatal(got)
	}
}
