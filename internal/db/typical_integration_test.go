// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

var typicalJobs atomic.Int64

// How long a run typically takes is the median from Generate to the result
// of an offer's done runs in the last 30 days, said only past five of them.
func TestTheTypicalTimeOfARunIsTheMedianOfEnoughRuns(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// The model the offers below are of.
	chatJob(t, store, 4701, "typical:seed:"+strconv.FormatInt(time.Now().UnixNano(), 10))
	for _, stmt := range []string{
		// Rare is a model of its own, with no kin to borrow a time from.
		`INSERT INTO catalog_model (id, kind, name, maker) VALUES ('test/rare', 'image', 'Rare', 'test') ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO catalog_offer (id, model_id, provider, provider_model_id, name, routes, fetched_at)
		 VALUES ('openrouter:test/slow', 'test/chat', 'openrouter', 'test/slow', 'Test: Slow', '[]', now()),
		        ('openrouter:test/rare', 'test/rare', 'openrouter', 'test/rare', 'Test: Rare', '[]', now())
		 ON CONFLICT (id) DO UPDATE SET model_id = EXCLUDED.model_id`,
		`DELETE FROM job WHERE offer_id IN ('openrouter:test/slow', 'openrouter:test/rare')`,
	} {
		if _, err := store.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	run := func(offer, status string, took, ago time.Duration) {
		t.Helper()
		job := chatJob(t, store, 4701, "typical:"+strconv.FormatInt(time.Now().UnixNano(), 10)+":"+strconv.FormatInt(typicalJobs.Add(1), 10))
		if _, err := store.pool.Exec(ctx, `
			UPDATE job SET offer_id = $2, status = $3, created_at = now() - make_interval(secs => $4::float8 + $5::float8),
			       queued_at = now() - make_interval(secs => $4::float8 + $5::float8),
			       finished_at = now() - make_interval(secs => $5::float8)
			WHERE id = $1`, job, offer, status, took.Seconds(), ago.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	// Five done runs: the median is the third, 40.2 s, rounded up.
	for _, took := range []float64{20, 30, 40.2, 50, 300} {
		run("openrouter:test/slow", "done", time.Duration(took*float64(time.Second)), time.Hour)
	}
	// A failed run and one from 31 days ago do not count.
	run("openrouter:test/slow", "failed", time.Second, time.Hour)
	run("openrouter:test/slow", "done", time.Second, 31*24*time.Hour)
	// Nor do runs whose times are out of order, as a row moved back by hand
	// to free a test's slot of the allowance: queued a day before it was
	// made, they would say a run takes a day.
	for range 3 {
		job := chatJob(t, store, 4701, "typical:"+strconv.FormatInt(time.Now().UnixNano(), 10)+":"+strconv.FormatInt(typicalJobs.Add(1), 10))
		if _, err := store.pool.Exec(ctx, `
			UPDATE job SET offer_id = 'openrouter:test/slow', status = 'done', created_at = now() - interval '1 hour',
			       queued_at = now() - interval '1 day', finished_at = now() - interval '59 minutes'
			WHERE id = $1`, job); err != nil {
			t.Fatal(err)
		}
	}
	// Four runs are too few to say.
	for range 4 {
		run("openrouter:test/rare", "done", 10*time.Second, time.Hour)
	}

	got, err := store.TypicalSeconds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["openrouter:test/slow"] != 41 {
		t.Errorf("slow = %d, want 41", got["openrouter:test/slow"])
	}
	if secs, ok := got["openrouter:test/rare"]; ok {
		t.Errorf("rare = %d from four runs", secs)
	}
}

// An offer with too few runs of its own borrows its model's time on every
// provider, and then that of its family's models of the same tier and kind
// on its provider; nothing further.
func TestATypicalTimeComesFromTheClosestKin(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	chatJob(t, store, 4702, "typical:kin:"+strconv.FormatInt(time.Now().UnixNano(), 10))
	family := func(tier string) string {
		return `{"family": {"id": "typkin", "name": "Kin", "tier": "` + tier + `"}}`
	}
	for _, stmt := range []string{
		`INSERT INTO catalog_model (id, kind, name, maker, traits) VALUES
		   ('test/kin-a', 'image', 'Kin A', 'test', '` + family("std") + `'),
		   ('test/kin-b', 'image', 'Kin B', 'test', '` + family("std") + `'),
		   ('test/kin-pro', 'image', 'Kin Pro', 'test', '` + family("pro") + `'),
		   ('test/kin-alone', 'image', 'Alone', 'test', '{}'),
		   ('test/kin-clip', 'video', 'Kin Clip', 'test', '` + family("std") + `'),
		   ('test/kin-film', 'video', 'Kin Film', 'test', '` + family("std") + `')
		 ON CONFLICT (id) DO UPDATE SET traits = EXCLUDED.traits`,
		`INSERT INTO catalog_offer (id, model_id, provider, provider_model_id, name, routes, fetched_at) VALUES
		   ('openrouter:test/kin-a', 'test/kin-a', 'openrouter', 'test/kin-a', 'A', '[]', now()),
		   ('gemini:test/kin-a', 'test/kin-a', 'gemini', 'test/kin-a', 'A direct', '[]', now()),
		   ('openrouter:test/kin-b', 'test/kin-b', 'openrouter', 'test/kin-b', 'B', '[]', now()),
		   ('openai:test/kin-b', 'test/kin-b', 'openai', 'test/kin-b', 'B elsewhere', '[]', now()),
		   ('openrouter:test/kin-pro', 'test/kin-pro', 'openrouter', 'test/kin-pro', 'Pro', '[]', now()),
		   ('openrouter:test/kin-alone', 'test/kin-alone', 'openrouter', 'test/kin-alone', 'Alone', '[]', now()),
		   ('openrouter:test/kin-clip', 'test/kin-clip', 'openrouter', 'test/kin-clip', 'Clip', '[]', now()),
		   ('openrouter:test/kin-film', 'test/kin-film', 'openrouter', 'test/kin-film', 'Film', '[]', now())
		 ON CONFLICT (id) DO NOTHING`,
		`DELETE FROM job WHERE offer_id LIKE '%:test/kin-%'`,
	} {
		if _, err := store.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	run := func(offer string, took float64) {
		t.Helper()
		job := chatJob(t, store, 4702, "typical:kin:"+strconv.FormatInt(time.Now().UnixNano(), 10)+":"+strconv.FormatInt(typicalJobs.Add(1), 10))
		if _, err := store.pool.Exec(ctx, `
			UPDATE job SET offer_id = $2, status = 'done', created_at = now() - make_interval(secs => $3::float8 + 3600),
			       queued_at = now() - make_interval(secs => $3::float8 + 3600), finished_at = now() - interval '1 hour'
			WHERE id = $1`, job, offer, took); err != nil {
			t.Fatal(err)
		}
	}
	for _, took := range []float64{10, 20, 30, 40, 50, 60} {
		run("openrouter:test/kin-a", took)
	}
	run("gemini:test/kin-a", 100)
	run("gemini:test/kin-a", 200)
	for _, took := range []float64{1, 1, 1} {
		run("openrouter:test/kin-alone", took)
	}
	// A video of the family takes minutes: its runs lend nothing to the
	// family's pictures.
	for range 5 {
		run("openrouter:test/kin-clip", 600)
	}

	got, err := store.TypicalSeconds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for offer, want := range map[string]int{
		"openrouter:test/kin-a":     35, // its own six runs
		"gemini:test/kin-a":         45, // two of its own: the model's eight
		"openrouter:test/kin-b":     35, // none: its family's std runs on OpenRouter
		"openai:test/kin-b":         0,  // none of the family's runs went through OpenAI
		"openrouter:test/kin-pro":   0,  // another tier is not the same speed
		"openrouter:test/kin-alone": 0,  // three runs, and no kin
		"openrouter:test/kin-clip":  600,
		"openrouter:test/kin-film":  600, // none: its family's videos of the tier
	} {
		if got[offer] != want {
			t.Errorf("%s = %d, want %d", offer, got[offer], want)
		}
	}
}
