// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The adapter in the real queue, against a fake Gemini:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/providers/gemini/
package gemini

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/migrations"
)

const person = int64(2011)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("FRAMY_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set FRAMY_TEST_DATABASE_URL to run the database tests")
	}
	// The tests truncate what they touch: never anything but scratch.
	if u, err := url.Parse(raw); err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatal("FRAMY_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\"")
	}
	ctx := context.Background()
	store, err := db.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "core-init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	pool := store.Pool()
	if _, err := pool.Exec(ctx, string(bootstrap)); err != nil {
		t.Fatalf("core bootstrap: %v", err)
	}
	if err := store.Migrate(ctx, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	of := offerOf(t, nano21)
	params, _ := json.Marshal(of.Params)
	routes, _ := json.Marshal(of.Routes)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`TRUNCATE framy.chat_run, framy.job_input, framy.asset, framy.job, framy.river_job`, nil},
		{`INSERT INTO core.person (telegram_user_id) VALUES ($1) ON CONFLICT DO NOTHING`, []any{person}},
		{`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ('google/gemini-nano-banana-2.1', 'image', 'Nano Banana 2.1', 'Google') ON CONFLICT DO NOTHING`, nil},
		{`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, params, routes, fetched_at)
		  VALUES ($1, 'google/gemini-nano-banana-2.1', 'gemini', $2, 'Nano Banana 2.1', $3, $4, now())
		  ON CONFLICT (id) DO UPDATE SET params = EXCLUDED.params, routes = EXCLUDED.routes`,
			[]any{of.ID, of.ProviderModelID, string(params), string(routes)}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	return pool
}

// sink keeps the results in memory.
type sink struct {
	mu   sync.Mutex
	kept map[int64][]jobs.Result
}

func (s *sink) Keep(_ context.Context, j jobs.Job, results []jobs.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept[j.ID] = append(s.kept[j.ID], results...)
	return nil
}

// links answers for a job's inputs the way storage does: a presigned link.
type links struct{ url string }

func (l links) Inputs(context.Context, jobs.Job) ([]jobs.Input, error) {
	return []jobs.Input{{ContentType: "image/jpeg", URL: l.url + "/in/1.jpg?X-Amz-Signature=sig"}}, nil
}

func TestTheQueueRecordsWhatGeminiAnswered(t *testing.T) {
	pool := testPool(t)
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0photo"))
	}))
	defer storage.Close()

	var mu sync.Mutex
	calls := map[string]int{}
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		input, _ := body["input"].([]any)
		prompt, _ := input[0].(map[string]any)["text"].(string)
		mu.Lock()
		calls[prompt]++
		bodies[prompt] = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(prompt, "unbilled"):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"quota_exceeded","message":"Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-nano-banana-2.1"}}`)
		case strings.Contains(prompt, "question"):
			_, _ = io.WriteString(w, answerWith(usage1K, textBlock("Which fox do you mean, the red or the arctic one?")))
		default:
			_, _ = io.WriteString(w, answerOK())
		}
	}))
	defer srv.Close()

	s := &sink{kept: map[int64][]jobs.Result{}}
	q, err := jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"gemini": New(Config{
			Base: srv.URL + "/v1beta", Fetch: storage.Client(),
			Key: func(context.Context, jobs.Job) (string, error) { return testKey, nil },
		})},
		Sink:      s,
		Inputs:    links{storage.URL},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		FetchPoll: 50 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	of := offerOf(t, nano21)
	of.ModelID = "google/gemini-nano-banana-2.1"
	estimate := price.Estimate{Offer: of.ID, USD: 0.0336, MaxUSD: 0.0336, Kind: price.About}
	enqueue := func(prompt string) int64 {
		id, _, err := q.Enqueue(ctx, jobs.New{
			UserID: person, Surface: jobs.DM, ChatID: person, Offer: of, Prompt: prompt,
			Settings: map[string]any{"resolution": "1K", "thinking_level": "high"}, Estimate: &estimate,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	made := enqueue("a fox in the snow")
	unbilled := enqueue("an unbilled key")
	asked := enqueue("a question")
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(stop)
	}()
	wait := func(id int64) jobs.Job {
		deadline := time.Now().Add(15 * time.Second)
		for {
			j, err := q.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status.Final() {
				return j
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %d still %s", id, j.Status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	j := wait(made)
	if j.Status != jobs.Done || j.ActualUSD == nil || !near(*j.ActualUSD, cost1K) || j.Submits != 1 {
		t.Errorf("the generation: %s, actual %v, %d submits, %s", j.Status, j.ActualUSD, j.Submits, j.Error)
	}
	s.mu.Lock()
	kept := len(s.kept[made])
	s.mu.Unlock()
	if kept != 1 {
		t.Errorf("%d results kept, want 1", kept)
	}

	// The reference came from the presigned link, inline, and the
	// settings reached Google as it names them.
	mu.Lock()
	body := bodies["a fox in the snow"]
	mu.Unlock()
	input, _ := body["input"].([]any)
	if len(input) != 2 || input[1].(map[string]any)["type"] != "image" {
		t.Errorf("input %v: the prompt and the reference", body["input"])
	}
	if gc, _ := body["generation_config"].(map[string]any); gc["thinking_level"] != "high" {
		t.Errorf("generation_config %v", body["generation_config"])
	}
	if body["model"] != of.ProviderModelID {
		t.Errorf("the request named model %v, want the offer's %q", body["model"], of.ProviderModelID)
	}

	// A project without billing: the job fails as a key at once. A retry
	// would leave it queued, and wait would time out.
	j = wait(unbilled)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Key || j.ActualUSD != nil || j.Submits != 1 {
		t.Errorf("the unbilled key: %s %s, actual %v, %d submits", j.Status, j.ErrorClass, j.ActualUSD, j.Submits)
	}
	if !strings.Contains(j.Error, "needs_billing") {
		t.Errorf("the status is lost: %q", j.Error)
	}
	mu.Lock()
	n := calls["an unbilled key"]
	mu.Unlock()
	if n != 1 {
		t.Errorf("%d calls: a key with no billing is not asked again", n)
	}

	// The model answered in words: a refusal that keeps them, and the cost
	// of the tokens it did use.
	j = wait(asked)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Moderation || j.ActualUSD == nil || j.Submits != 1 {
		t.Errorf("the question: %s %s, actual %v, %d submits", j.Status, j.ErrorClass, j.ActualUSD, j.Submits)
	}
	if !strings.Contains(j.Error, "Which fox do you mean, the red or the arctic one?") {
		t.Errorf("the model's words are lost: %q", j.Error)
	}
}

func TestTheQueueRunsAnOmniVideoAndKeepsTheGrounding(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	video := offerOf(t, omni)
	params, _ := json.Marshal(video.Params)
	routes, _ := json.Marshal(video.Routes)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ('google/gemini-omni-1.1-flash', 'video', 'Gemini Omni 1.1 Flash', 'Google') ON CONFLICT DO NOTHING`, nil},
		{`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, params, routes, fetched_at)
		  VALUES ($1, 'google/gemini-omni-1.1-flash', 'gemini', $2, 'Gemini Omni 1.1 Flash', $3, $4, now())
		  ON CONFLICT (id) DO UPDATE SET params = EXCLUDED.params, routes = EXCLUDED.routes`,
			[]any{video.ID, video.ProviderModelID, string(params), string(routes)}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0photo"))
	}))
	defer storage.Close()

	suggestions := `<div class="container"><a class="chip" href="https://www.google.com/search?q=arctic+fox">arctic fox</a></div>`
	var mu sync.Mutex
	posts, gets := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// Running at the first poll, done at the second.
			gets++
			if gets == 1 {
				_, _ = io.WriteString(w, running)
				return
			}
			_, _ = io.WriteString(w, finished(videoBlock(mp4)))
			return
		}
		posts++
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["model"] == omni {
			if body["background"] != true || body["store"] != true {
				t.Errorf("a video not in the background: %v", body)
			}
			_, _ = io.WriteString(w, running)
			return
		}
		_, _ = io.WriteString(w, `{"id":"v1_g","status":"completed","steps":[`+
			`{"type":"google_search_call","arguments":{"queries":["arctic fox"]}},`+
			`{"type":"google_search_result","result":[{"search_suggestions":`+mustJSON(suggestions)+`}]},`+
			`{"type":"model_output","content":[`+imageBlock(png, "image/png")+`]}],"usage":`+usage1K+`}`)
	}))
	defer srv.Close()

	s := &sink{kept: map[int64][]jobs.Result{}}
	q, err := jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"gemini": New(Config{
			Base: srv.URL + "/v1beta", HTTP: srv.Client(), Fetch: storage.Client(),
			Key: func(context.Context, jobs.Job) (string, error) { return testKey, nil },
		})},
		Sink:      s,
		Inputs:    links{storage.URL},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		FetchPoll: 50 * time.Millisecond,
		PollEvery: 100 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	video.ModelID = "google/gemini-omni-1.1-flash"
	image := offerOf(t, nano21)
	image.ModelID = "google/gemini-nano-banana-2.1"
	estimate := price.Estimate{Offer: video.ID, USD: 0.30, MaxUSD: 1.02, Kind: price.UpTo}
	vid, _, err := q.Enqueue(ctx, jobs.New{
		UserID: person, Surface: jobs.Site, Offer: video, Prompt: "an arctic fox running",
		Settings: map[string]any{"resolution": "720p"}, Estimate: &estimate,
	})
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := q.Enqueue(ctx, jobs.New{
		UserID: person, Surface: jobs.Site, Offer: image, Prompt: "an arctic fox, as it really looks",
		Settings: map[string]any{"image_search": true}, Estimate: &price.Estimate{Offer: image.ID, USD: 0.04, MaxUSD: 0.04, Kind: price.About},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(stop)
	}()
	wait := func(id int64) jobs.Job {
		deadline := time.Now().Add(20 * time.Second)
		for {
			j, err := q.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status.Final() {
				return j
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %d still %s", id, j.Status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	j := wait(vid)
	if j.Status != jobs.Done || j.Submits != 1 || j.ProviderJobID != "v1_ChdvbW5pMTIz" || j.ActualUSD == nil || !near(*j.ActualUSD, omniCost) {
		t.Errorf("the video: %s, %d submits, id %q, actual %v, %s", j.Status, j.Submits, j.ProviderJobID, j.ActualUSD, j.Error)
	}
	s.mu.Lock()
	kept := s.kept[vid]
	s.mu.Unlock()
	if len(kept) != 1 || kept[0].ContentType != "video/mp4" || string(kept[0].Data) != string(mp4) {
		t.Errorf("kept %v", kept)
	}
	mu.Lock()
	if gets != 2 {
		t.Errorf("%d polls, want 2", gets)
	}
	mu.Unlock()

	j = wait(img)
	if j.Status != jobs.Done {
		t.Fatalf("the grounded image: %s %s", j.Status, j.Error)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT grounding FROM framy.job WHERE id = $1`, img).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var g jobs.Grounding
	if err := json.Unmarshal(raw, &g); err != nil || len(g.Suggestions) != 1 || g.Suggestions[0] != suggestions || len(g.Queries) != 1 {
		t.Errorf("grounding %s (%v): the suggestions Google asks to show are kept with the job", raw, err)
	}
	var none []byte
	if err := pool.QueryRow(ctx, `SELECT grounding FROM framy.job WHERE id = $1`, vid).Scan(&none); err != nil || none != nil {
		t.Errorf("the video's grounding %s (%v), want none", none, err)
	}
}
