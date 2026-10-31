// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// mp4 is the smallest file storage calls a video: an ISO BMFF box.
var mp4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")

// fakeKey is the key the fake accepts for downloads unless a test says
// another.
const fakeKey = "sk-or-v1-fake-key-for-tests"

// fakeOR is a fake OpenRouter Videos API, built from the shapes in
// docs/research/video.md: POST /videos answers 202 with the job's id,
// GET /videos/{id} the job object, GET /videos/{id}/content the file for
// whoever holds the key.
type fakeOR struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// What it was asked.
	submits   []map[string]any
	submitted []http.Header
	polls     []string // the Authorization of each GET /videos/{id}
	downloads []string // the Authorization of each GET .../content
	queries   []string // the query of each GET .../content
	// What it answers.
	id          string
	status      string          // the job's status; pending until a test says otherwise
	errorJSON   json.RawMessage // the job's "error", raw
	cost        *float64
	urls        int
	urlHost     string // where the job's unsigned_urls point; the fake itself when empty
	video       []byte
	key         string // the key a download needs; fakeKey by default
	submitReply func(w http.ResponseWriter)
	pollReply   func(w http.ResponseWriter)
	contentFunc func(w http.ResponseWriter, r *http.Request)
}

func newFakeOR(t *testing.T) *fakeOR {
	t.Helper()
	f := &fakeOR{t: t, id: "gen-vid-1761555600-abcdefghij0123456789", status: videoPending, urls: 1, video: mp4, key: fakeKey}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/videos", f.submit)
	mux.HandleFunc("GET /api/v1/videos/{id}", f.poll)
	mux.HandleFunc("GET /api/v1/videos/{id}/content", f.content)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOR) base() string { return f.srv.URL + "/api/v1" }

// complete finishes the job with one video at the given cost.
func (f *fakeOR) complete(cost float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.cost = videoCompleted, &cost
}

// set changes what the job says.
func (f *fakeOR) set(status string, errorJSON string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	f.errorJSON = json.RawMessage(errorJSON)
}

func (f *fakeOR) submit(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		f.t.Errorf("submit body: %v", err)
	}
	f.mu.Lock()
	f.submits = append(f.submits, body)
	f.submitted = append(f.submitted, r.Header.Clone())
	reply, id := f.submitReply, f.id
	f.mu.Unlock()
	if reply != nil {
		reply(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"id":%q,"polling_url":%q,"status":"pending","generation_id":"gen-1"}`, id, f.base()+"/videos/"+id)
}

func (f *fakeOR) poll(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.polls = append(f.polls, r.Header.Get("Authorization"))
	reply := f.pollReply
	job := f.jobObject(r.PathValue("id"))
	f.mu.Unlock()
	if reply != nil {
		reply(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(job)
}

// jobObject is the job as OpenRouter's polling answers; f.mu is held.
func (f *fakeOR) jobObject(id string) []byte {
	job := map[string]any{"id": id, "generation_id": "gen-1", "polling_url": f.base() + "/videos/" + id, "status": f.status}
	if f.status == videoCompleted {
		host := f.base()
		if f.urlHost != "" {
			host = f.urlHost
		}
		var urls []string
		for i := range f.urls {
			urls = append(urls, fmt.Sprintf("%s/videos/%s/content?index=%d", host, id, i))
		}
		job["unsigned_urls"] = urls
	}
	if len(f.errorJSON) > 0 {
		job["error"] = f.errorJSON
	}
	if f.cost != nil {
		job["usage"] = map[string]any{"cost": *f.cost, "is_byok": false}
	}
	b, _ := json.Marshal(job)
	return b
}

func (f *fakeOR) content(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	auth := r.Header.Get("Authorization")
	f.downloads = append(f.downloads, auth)
	f.queries = append(f.queries, r.URL.RawQuery)
	custom, video, key := f.contentFunc, f.video, f.key
	f.mu.Unlock()
	if custom != nil {
		custom(w, r)
		return
	}
	if auth != "Bearer "+key {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"Missing Authentication header","metadata":{"error_type":"authentication"}}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	_, _ = w.Write(video)
}

func (f *fakeOR) counts() (submits, polls, downloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.submits), len(f.polls), len(f.downloads)
}

// authorizations are the Authorization headers the fake saw, by call.
func (f *fakeOR) authorizations() (submits, polls, downloads []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.submitted {
		submits = append(submits, h.Get("Authorization"))
	}
	return submits, append([]string(nil), f.polls...), append([]string(nil), f.downloads...)
}

func (f *fakeOR) lastSubmit() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.submits) == 0 {
		f.t.Fatal("no video request was sent")
	}
	return f.submits[len(f.submits)-1]
}

// videoOffer is a video model like HeyGen's, with what the other models add:
// last frames, audio and a seed, sizes, and a pass-through negative prompt.
func videoOffer() catalog.Offer {
	return catalog.Offer{
		ID: "openrouter:test/video", ModelID: "test/video", Provider: catalog.OpenRouter, ProviderModelID: "test/video", Kind: catalog.Video,
		Params: map[string]catalog.Param{
			"resolution":      {Type: catalog.Enum, Values: []any{"480p", "768p", "2K"}},
			"aspect_ratio":    {Type: catalog.Enum, Values: []any{"16:9", "9:16", "1:1"}},
			"size":            {Type: catalog.Enum, Values: []any{"1280x720", "720x1280"}},
			"duration":        {Type: catalog.Enum, Values: []any{5.0, 6.0, 8.0, 10.0}},
			"frame_images":    {Type: catalog.Enum, Values: []any{"first_frame", "last_frame"}},
			"generate_audio":  {Type: catalog.Boolean},
			"seed":            {Type: catalog.Boolean},
			"negative_prompt": {Type: catalog.Text, Passthrough: true},
		},
		Routes: []catalog.Route{{Passthrough: []string{"negative_prompt"}, SKUs: map[string]string{"duration_seconds_768p": "0.036"}}},
	}
}

func videoJobOf(settings map[string]any, inputs ...jobs.Input) jobs.Job {
	of := videoOffer()
	return jobs.Job{
		ID: 7, UserID: 1001, OfferID: of.ID, ModelID: of.ModelID, Provider: of.Provider, Kind: catalog.Video,
		Prompt: "a fox in the snow", Settings: settings, Free: true, Offer: of, Inputs: inputs,
	}
}

// fakeProvider is the adapter pointed at the fake; tweak changes its Config.
func fakeProvider(f *fakeOR, cb Callbacks, tweak ...func(*Config)) *Provider {
	cfg := Config{
		Base:      f.base(),
		HTTP:      f.srv.Client(),
		Title:     "framy",
		Callbacks: cb,
		Key:       func(context.Context, jobs.Job) (string, error) { return fakeKey, nil },
	}
	for _, t := range tweak {
		t(&cfg)
	}
	return New(cfg)
}

// jobDoc is the job object as the fake would answer now, for a test that
// writes the answer itself.
func (f *fakeOR) jobDoc() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobObject(f.id)
}

// testCallbacks are the callbacks of a server at framy.example.
func testCallbacks() Callbacks {
	return Callbacks{PublicURL: "https://framy.example", Secret: []byte(strings.Repeat("s3cret-", 8))}
}
