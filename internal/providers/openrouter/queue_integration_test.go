// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The adapter in the real queue, against a fake OpenRouter:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/providers/openrouter/
package openrouter

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

const person = int64(2001)

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
	of := offer()
	params, _ := json.Marshal(of.Params)
	routes, _ := json.Marshal(of.Routes)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`TRUNCATE framy.site_delivery, framy.chat_run, framy.job_input, framy.asset, framy.job, framy.river_job`, nil},
		{`INSERT INTO core.person (telegram_user_id) VALUES ($1) ON CONFLICT DO NOTHING`, []any{person}},
		{`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ($1, 'image', 'Test image', 'test') ON CONFLICT DO NOTHING`, []any{of.ModelID}},
		{`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, params, routes, fetched_at)
		  VALUES ($1, $2, 'openrouter', $3, 'Test image', $4, $5, now())
		  ON CONFLICT (id) DO UPDATE SET params = EXCLUDED.params, routes = EXCLUDED.routes`,
			[]any{of.ID, of.ModelID, of.ProviderModelID, string(params), string(routes)}},
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

func TestTheQueueRecordsWhatOpenRouterAnswered(t *testing.T) {
	pool := testPool(t)
	var mu sync.Mutex
	models := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		prompt, _ := body["prompt"].(string)
		mu.Lock()
		models[prompt], _ = body["model"].(string)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(prompt, "moderated") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"message":"Your input was flagged. No credits were charged.","metadata":{"reasons":["sexual"],"flagged_input":"x","provider_name":"Azure"}}}`)
			return
		}
		_, _ = io.WriteString(w, images("0.0195", png))
	}))
	defer srv.Close()

	s := &sink{kept: map[int64][]jobs.Result{}}
	q, err := jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openrouter": New(Config{
			Base: srv.URL,
			Key:  func(context.Context, jobs.Job) (string, error) { return "sk-or-v1-test", nil },
		})},
		Sink:      s,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		FetchPoll: 50 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	of := offer()
	estimate := price.Estimate{Offer: of.ID, Route: "cheap", USD: 0.0195, MaxUSD: 0.0195, Kind: price.Exact}
	enqueue := func(prompt string) int64 {
		id, _, err := q.Enqueue(ctx, jobs.New{
			UserID: person, Surface: jobs.DM, ChatID: person, Offer: of, Prompt: prompt,
			Settings: map[string]any{"resolution": "2K"}, Free: true, Estimate: &estimate,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	made := enqueue("a fox in the snow")
	refused := enqueue("a moderated prompt")
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(stop)
	}()
	wait := func(id int64) jobs.Job {
		deadline := time.Now().Add(10 * time.Second)
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
	if j.Status != jobs.Done || j.ActualUSD == nil || *j.ActualUSD != 0.0195 || j.Submits != 1 {
		t.Errorf("the generation: %s, actual %v, %d submits, %s", j.Status, j.ActualUSD, j.Submits, j.Error)
	}
	s.mu.Lock()
	kept := len(s.kept[made])
	s.mu.Unlock()
	if kept != 1 {
		t.Errorf("%d results kept, want 1", kept)
	}

	j = wait(refused)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Moderation || j.ActualUSD != nil || j.Submits != 1 {
		t.Errorf("the moderated prompt: %s %s, actual %v, %d submits", j.Status, j.ErrorClass, j.ActualUSD, j.Submits)
	}
	if !strings.Contains(j.Error, "sexual") {
		t.Errorf("the reason is lost: %q", j.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if models["a fox in the snow"] != of.ProviderModelID {
		t.Errorf("the request named model %q, want the offer's %q", models["a fox in the snow"], of.ProviderModelID)
	}
}
