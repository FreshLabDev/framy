// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The adapter in the real queue, against a fake OpenAI:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/providers/openai/
package openai

import (
	"context"
	"encoding/json"
	"errors"
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
	m, of := realOffer(t, flare)
	params, _ := json.Marshal(of.Params)
	routes, _ := json.Marshal(of.Routes)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`TRUNCATE framy.job_input, framy.asset, framy.job, framy.river_job`, nil},
		{`INSERT INTO core.person (telegram_user_id) VALUES ($1) ON CONFLICT DO NOTHING`, []any{person}},
		{`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ($1, 'image', $2, 'OpenAI') ON CONFLICT DO NOTHING`, []any{m.ID, m.Name}},
		{`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, params, routes, fetched_at)
		  VALUES ($1, $2, 'openai', $3, $4, $5, $6, now())
		  ON CONFLICT (id) DO UPDATE SET params = EXCLUDED.params, routes = EXCLUDED.routes`,
			[]any{of.ID, m.ID, of.ProviderModelID, of.Name, string(params), string(routes)}},
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

// links stands in for storage: a presigned link for each of a job's files.
type links struct{}

func (links) Inputs(_ context.Context, j jobs.Job) ([]jobs.Input, error) {
	if !strings.Contains(j.Prompt, "edit") {
		return nil, nil
	}
	return []jobs.Input{
		{ContentType: "image/jpeg", URL: "https://acct.r2.cloudflarestorage.com/framy/u/1/in/a.jpg?X-Amz-Signature=aaa"},
		{ContentType: "image/png", URL: "https://acct.r2.cloudflarestorage.com/framy/u/1/in/b.png?X-Amz-Signature=bbb"},
	}, nil
}

func TestTheQueueRecordsWhatOpenAIAnswered(t *testing.T) {
	pool := testPool(t)
	var mu sync.Mutex
	paths := map[string]string{}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		prompt, _ := body["prompt"].(string)
		mu.Lock()
		paths[prompt] = r.URL.Path
		calls++
		mu.Unlock()
		if body["safety_identifier"] != jobs.SafetyID(person) || body["quality"] != "medium" || body["model"] != "gpt-image-2.5-flare" {
			t.Errorf("request %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(prompt, "moderated"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"moderation_blocked","message":"Your request was rejected by the safety system.","type":"image_generation_user_error","moderation_details":{"moderation_stage":"input","categories":["sexual"]}}}`)
		case strings.Contains(prompt, "broke"):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"You exceeded your current quota.","type":"insufficient_quota","code":"insufficient_quota"}}`)
		case strings.Contains(prompt, "edit"):
			_, _ = io.WriteString(w, images(20, 1360, 439, png))
		default:
			_, _ = io.WriteString(w, images(12, 0, 439, png))
		}
	}))
	defer srv.Close()

	s := &sink{kept: map[int64][]jobs.Result{}}
	q, err := jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openai": New(Config{
			Base: srv.URL + "/v1",
			Key:  func(context.Context, jobs.Job) (string, error) { return testKey, nil },
		})},
		Sink:      s,
		Inputs:    links{},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		FetchPoll: 50 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, of := realOffer(t, flare)
	enqueue := func(prompt string) int64 {
		settings := map[string]any{"size": "1024x1024", "quality": "medium"}
		est := price.Of(m, of, price.Request{Settings: settings, Prompt: prompt})
		id, _, err := q.Enqueue(ctx, jobs.New{
			UserID: person, Surface: jobs.DM, ChatID: person, Offer: of, Prompt: prompt, Settings: settings, Estimate: &est,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	made := enqueue("a fox in the snow")
	edited := enqueue("edit the fox into a wolf")
	refused := enqueue("a moderated prompt")
	broke := enqueue("a prompt for a person who broke the limit")
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
	wantMade := 12*5e-6 + 439*30e-6
	if j.Status != jobs.Done || j.ActualUSD == nil || !near(*j.ActualUSD, wantMade) || j.Submits != 1 {
		t.Errorf("the generation: %s, actual %v, %d submits, %s", j.Status, j.ActualUSD, j.Submits, j.Error)
	}
	if j.EstimateUSD == nil || j.ActualUSD == nil || !within(*j.ActualUSD, *j.EstimateUSD, 0.2) {
		t.Errorf("the generation: actual %v against estimate %v: more than 20%% apart", j.ActualUSD, j.EstimateUSD)
	}

	j = wait(edited)
	wantEdit := 20*5e-6 + 1360*8e-6 + 439*30e-6
	if j.Status != jobs.Done || j.ActualUSD == nil || !near(*j.ActualUSD, wantEdit) {
		t.Errorf("the edit: %s, actual %v, want %v, %s", j.Status, j.ActualUSD, wantEdit, j.Error)
	}
	s.mu.Lock()
	kept := len(s.kept[made]) + len(s.kept[edited])
	s.mu.Unlock()
	if kept != 2 {
		t.Errorf("%d results kept, want 2", kept)
	}

	j = wait(refused)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Moderation || j.ActualUSD != nil || j.Submits != 1 {
		t.Errorf("the moderated prompt: %s %s, actual %v, %d submits", j.Status, j.ErrorClass, j.ActualUSD, j.Submits)
	}
	if !strings.Contains(j.Error, "input stage") || !strings.Contains(j.Error, "sexual") {
		t.Errorf("the stage and the category are lost: %q", j.Error)
	}

	j = wait(broke)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Money || j.ActualUSD != nil || j.Submits != 1 {
		t.Errorf("the run with no credit: %s %s, actual %v, %d submits: money is never retried", j.Status, j.ErrorClass, j.ActualUSD, j.Submits)
	}

	mu.Lock()
	defer mu.Unlock()
	if paths["a fox in the snow"] != "/v1/images/generations" || paths["edit the fox into a wolf"] != "/v1/images/edits" {
		t.Errorf("endpoints %v", paths)
	}
	if calls != 4 {
		t.Errorf("%d requests for 4 jobs", calls)
	}
}

func TestAJobWithNoKeyFailsAsKeyBeforeAnythingIsSent(t *testing.T) {
	pool := testPool(t)
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
	}))
	defer srv.Close()
	q, err := jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openai": New(Config{
			Base: srv.URL + "/v1",
			Key: func(context.Context, jobs.Job) (string, error) {
				return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenAI key connected yet")}
			},
		})},
		Sink:      &sink{kept: map[int64][]jobs.Result{}},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		FetchPoll: 50 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, of := realOffer(t, flare)
	id, _, err := q.Enqueue(ctx, jobs.New{
		UserID: person, Surface: jobs.DM, ChatID: person, Offer: of, Prompt: "a fox",
		Settings: map[string]any{"size": "1024x1024", "quality": "medium"},
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
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, err := q.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status.Final() {
			if j.Status != jobs.Failed || j.ErrorClass != jobs.Key || !strings.Contains(j.Error, "no OpenAI key connected yet") || j.ActualUSD != nil {
				t.Errorf("%s %s %q", j.Status, j.ErrorClass, j.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", j.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("%d requests went out without a key", calls)
	}
}
