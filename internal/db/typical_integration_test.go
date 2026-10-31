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
		`INSERT INTO catalog_offer (id, model_id, provider, provider_model_id, name, routes, fetched_at)
		 VALUES ('openrouter:test/slow', 'test/chat', 'openrouter', 'test/slow', 'Test: Slow', '[]', now()),
		        ('openrouter:test/rare', 'test/chat', 'openrouter', 'test/rare', 'Test: Rare', '[]', now())
		 ON CONFLICT (id) DO NOTHING`,
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
			UPDATE job SET offer_id = $2, status = $3, queued_at = now() - make_interval(secs => $4::float8 + $5::float8),
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
