// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// The catalog's own fixtures: OpenRouter's listings of 27 October 2026.
const catalogFixtures = "../../catalog/testdata/openrouter"

// fixtureOffers builds the catalog from the fixtures, the way the sync does.
func fixtureOffers(t *testing.T) []catalog.Offer {
	t.Helper()
	listing := map[string]string{
		"/api/v1/models":        "models.json",
		"/api/v1/images/models": "images.json",
		"/api/v1/videos/models": "videos.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := listing[r.URL.Path]; ok {
			http.ServeFile(w, r, filepath.Join(catalogFixtures, name))
			return
		}
		id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/images/models/"), "/endpoints")
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(catalogFixtures, "endpoints", strings.ReplaceAll(id, "/", "_")+".json"))
	}))
	t.Cleanup(srv.Close)
	up, err := (&catalog.OpenRouterSource{Base: srv.URL + "/api/v1", HTTP: srv.Client(), Parallel: 4}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return up.Offers
}

func offerNamed(t *testing.T, offers []catalog.Offer, id string) catalog.Offer {
	t.Helper()
	for _, of := range offers {
		if of.ID == id {
			return of
		}
	}
	t.Fatalf("offer %s is not in the fixtures", id)
	return catalog.Offer{}
}

// stored is what a job's settings are after the database: JSON, so numbers are
// float64 and lists are []any.
func stored(t *testing.T, settings map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The free video plans fix their settings in internal/free; the adapter must
// take them as they are, against the catalog's own parameters, or the weekly
// video would be refused for every person.
func TestTheFreeVideoPlansRunThroughTheAdapter(t *testing.T) {
	offers := fixtureOffers(t)
	photo := jobs.Input{ContentType: "image/jpeg", URL: "https://files.example/photo.jpg?X-Amz-Signature=sigA"}
	clip := jobs.Input{ContentType: "video/mp4", URL: "https://files.example/clip.mp4?X-Amz-Signature=sigB"}
	frame := func(link string) string {
		return `"frame_images":[{"frame_type":"first_frame","image_url":{"url":"` + link + `"},"type":"image_url"}]`
	}

	for _, tc := range []struct {
		name   string
		plan   free.Plan
		inputs []jobs.Input
		want   string
	}{
		{"heygen from text", free.VideoHeyGen, nil,
			`{"duration":5,"model":"heygen/heygen-video-1","prompt":"a fox in the snow","resolution":"768p"}`},
		{"heygen from a photo", free.VideoHeyGen, []jobs.Input{photo},
			`{"duration":5,` + frame(photo.URL) + `,"model":"heygen/heygen-video-1","prompt":"a fox in the snow","resolution":"768p"}`},
		{"grok from text", free.VideoGrok, nil,
			`{"duration":5,"model":"x-ai/grok-imagine-video-1.5-lite","prompt":"a fox in the snow","resolution":"720p"}`},
		{"grok from a photo", free.VideoGrok, []jobs.Input{photo},
			`{"duration":5,` + frame(photo.URL) + `,"model":"x-ai/grok-imagine-video-1.5-lite","prompt":"a fox in the snow","resolution":"720p"}`},
		{"flux edit of a clip", free.VideoEdit, []jobs.Input{clip},
			`{"input_references":[{"type":"video_url","video_url":{"url":"` + clip.URL + `"}}],"model":"black-forest-labs/flux-video-edit","prompt":"a fox in the snow"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			of := offerNamed(t, offers, tc.plan.Offer)
			if of.Kind != catalog.Video {
				t.Fatalf("%s is %s", of.ID, of.Kind)
			}
			j := jobs.Job{
				ID: 7, UserID: 1001, OfferID: of.ID, ModelID: of.ModelID, Provider: of.Provider, Kind: of.Kind,
				Prompt: "a fox in the snow", Free: true, Offer: of, Inputs: tc.inputs,
				Settings: stored(t, tc.plan.SettingsFor(len(tc.inputs))),
			}
			body, err := videoRequest(j, "")
			if err != nil {
				t.Fatalf("the plan's own settings are refused: %v", err)
			}
			if got := bodyJSON(t, body); got != tc.want {
				t.Errorf("body\n got %s\nwant %s", got, tc.want)
			}

			// And the whole way: the fake takes it and the job is on its way.
			f := newFakeOR(t)
			sub, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), j)
			if err != nil || sub.ProviderJobID != f.id {
				t.Fatalf("submission %+v, error %v", sub, err)
			}
			sent := f.lastSubmit()
			if sent["callback_url"] == nil || sent["model"] != of.ProviderModelID {
				t.Errorf("sent %v", sent)
			}
		})
	}
}

// Without frame_images a photo goes as a reference, and OpenRouter prices a
// reference apart: HeyGen's 768p is $0.036 a second as a start frame and $0.10
// with references. The free plan names the photo as a frame so the budget it
// was counted with holds.
func TestAPhotoWithoutAFrameTypeIsAReference(t *testing.T) {
	offers := fixtureOffers(t)
	of := offerNamed(t, offers, free.VideoHeyGen.Offer)
	photo := jobs.Input{ContentType: "image/jpeg", URL: "https://files.example/photo.jpg"}
	j := jobs.Job{
		ID: 7, Kind: catalog.Video, Prompt: "x", Free: true, Offer: of, Inputs: []jobs.Input{photo},
		Settings: stored(t, free.VideoHeyGen.Settings()),
	}
	body, err := videoRequest(j, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["input_references"]; !ok {
		t.Errorf("body %v", body)
	}
	if _, ok := body["frame_images"]; ok {
		t.Errorf("body %v", body)
	}
}
