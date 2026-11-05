// SPDX-License-Identifier: Apache-2.0
//go:build integration

package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stackQueue is the queue of one of two processes on one schema, as
// production and the test bot are (docs/design.md, Stacks).
func stackQueue(t *testing.T, pool *pgxpool.Pool, stack string, p *fakeProvider) *Queue {
	t.Helper()
	q, err := NewQueue(pool, Config{
		Stack:     stack,
		Providers: map[string]Provider{"openrouter": p},
		Sink:      p,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		PollEvery: 100 * time.Millisecond,
		StopGrace: 5 * time.Second,
		FetchPoll: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestEachStackWorksOnlyItsOwnJobs(t *testing.T) {
	pool := testPool(t)
	prodFake, testFake := newFake(), newFake()
	prod := stackQueue(t, pool, "", prodFake)
	test := stackQueue(t, pool, "test", testFake)
	mine := enqueue(t, prod, job(alice, imageOffer))
	theirs := enqueue(t, test, job(bob, imageOffer))

	for _, c := range []struct {
		id           int64
		stack, queue string
	}{{mine, "", QueueOpenRouterImage}, {theirs, "test", "test_" + QueueOpenRouterImage}} {
		if got := scalar[string](t, pool, `SELECT stack FROM framy.job WHERE id = $1`, c.id); got != c.stack {
			t.Errorf("job %d is on stack %q, want %q", c.id, got, c.stack)
		}
		got := scalar[string](t, pool, `SELECT r.queue FROM framy.river_job r JOIN framy.job j ON j.river_job_id = r.id WHERE j.id = $1`, c.id)
		if got != c.queue {
			t.Errorf("job %d is in queue %q, want %q", c.id, got, c.queue)
		}
	}

	// Only the test bot's process runs: its job is done, production's waits.
	start(t, test)
	eventually(t, "the test bot's job done", func() bool { return get(t, test, theirs).Status == Done })
	time.Sleep(500 * time.Millisecond)
	if j := get(t, prod, mine); j.Status != Queued || testFake.count(testFake.submits, mine) != 0 {
		t.Fatalf("production's job: %s, sent %d times by the test bot", j.Status, testFake.count(testFake.submits, mine))
	}

	start(t, prod)
	eventually(t, "production's job done", func() bool { return get(t, prod, mine).Status == Done })
	if prodFake.count(prodFake.submits, theirs) != 0 || prodFake.count(prodFake.submits, mine) != 1 || testFake.count(testFake.submits, theirs) != 1 {
		t.Errorf("sent: production %d of its own, %d of the test bot's; test %d of its own",
			prodFake.count(prodFake.submits, mine), prodFake.count(prodFake.submits, theirs), testFake.count(testFake.submits, theirs))
	}
}

// A person's slots and turns are counted on each stack apart: the runs a
// stopped test bot left running and waiting hold up nothing of the same
// person's on production, and production leaves them alone.
func TestAStoppedStackHoldsUpNothingOnTheOther(t *testing.T) {
	pool := testPool(t)
	prodFake := newFake()
	prod := stackQueue(t, pool, "", prodFake)
	test := stackQueue(t, pool, "test", newFake())
	// The test bot stopped with two of alice's videos at the provider and an
	// image of hers waiting behind them.
	t1 := enqueue(t, test, job(alice, videoOffer))
	t2 := enqueue(t, test, job(alice, videoOffer))
	t3 := enqueue(t, test, job(alice, imageOffer))
	if _, err := pool.Exec(context.Background(), `
		UPDATE framy.job SET status = 'running', started_at = now(), submitted_at = now(), provider_job_id = 'gen-' || id
		WHERE id = ANY($1)`, []int64{t1, t2}); err != nil {
		t.Fatal(err)
	}

	p1 := enqueue(t, prod, job(alice, imageOffer))
	p2 := enqueue(t, prod, job(alice, imageOffer))
	start(t, prod)
	for _, id := range []int64{p1, p2} {
		eventually(t, "alice's jobs on production done", func() bool { return get(t, prod, id).Status == Done })
	}
	for id, want := range map[int64]Status{t1: Running, t2: Running, t3: Queued} {
		if j := get(t, prod, id); j.Status != want || prodFake.count(prodFake.submits, id)+prodFake.count(prodFake.polls, id) != 0 {
			t.Errorf("the test bot's job %d: %s, touched %d times by production, want %s untouched", id, j.Status,
				prodFake.count(prodFake.submits, id)+prodFake.count(prodFake.polls, id), want)
		}
	}
}

// Each stack's process answers in its metrics for its own jobs: the jobs
// waiting, and the jobs its sweep ended. Scraped together, the two add up
// to the whole and count no job twice.
func TestEachStackCountsOnlyItsOwnJobs(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	prod := stackQueue(t, pool, "", newFake())
	test := stackQueue(t, pool, "test", newFake())
	mine := enqueue(t, prod, job(alice, imageOffer))
	theirs := enqueue(t, test, job(bob, imageOffer))
	enqueue(t, test, job(bob, imageOffer))

	for q, want := range map[*Queue]float64{prod: 1, test: 2} {
		if waiting, err := q.Waiting(ctx); err != nil || waiting["queued"] != want {
			t.Errorf("stack %q: waiting %v, %v; want %v queued", q.cfg.Stack, waiting, err, want)
		}
	}

	// River gave up on one job of each stack; production's process sweeps
	// both, and counts only its own.
	if _, err := pool.Exec(ctx, `UPDATE framy.river_job SET state='discarded', finalized_at=now()
		WHERE id IN (SELECT river_job_id FROM framy.job WHERE id = ANY($1))`, []int64{mine, theirs}); err != nil {
		t.Fatal(err)
	}
	internal := `framy_job_failures_total{reason="internal"}`
	before := sample(t, internal)
	if n, err := prod.sweep(ctx); err != nil || n != 2 {
		t.Fatalf("sweep = %d, %v; want 2", n, err)
	}
	if got := sample(t, internal) - before; got != 1 {
		t.Errorf("the sweep counted %v failures, want 1: production's own", got)
	}
	for _, id := range []int64{mine, theirs} {
		if j := get(t, prod, id); j.Status != Failed {
			t.Errorf("job %d: %s, want failed by the sweep", id, j.Status)
		}
	}
}
