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

// A person's runs at once are counted over both stacks, and the job that ends
// on one wakes the next one waiting on the other.
func TestAPersonsLimitHoldsAcrossStacks(t *testing.T) {
	pool := testPool(t)
	release := make(chan struct{})
	prodFake, testFake := newFake(), newFake()
	prodFake.submit = func(ctx context.Context, _ Job) (Submission, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return Submission{}, ctx.Err()
		}
		return done(0.02), nil
	}
	prod := stackQueue(t, pool, "", prodFake)
	test := stackQueue(t, pool, "test", testFake)
	a1 := enqueue(t, prod, job(alice, imageOffer))
	a2 := enqueue(t, prod, job(alice, imageOffer))
	start(t, prod)
	eventually(t, "alice's two jobs running on production", func() bool { return prodFake.count(prodFake.inFlight, alice) == 2 })

	a3 := enqueue(t, test, job(alice, imageOffer))
	start(t, test)
	time.Sleep(500 * time.Millisecond)
	if j := get(t, test, a3); j.Status != Queued || testFake.count(testFake.submits, a3) != 0 {
		t.Fatalf("the third job: %s, %d requests", j.Status, testFake.count(testFake.submits, a3))
	}
	close(release)
	for _, id := range []int64{a1, a2, a3} {
		eventually(t, "every job done", func() bool { return get(t, prod, id).Status == Done })
	}
	if testFake.count(testFake.submits, a3) != 1 || prodFake.count(prodFake.submits, a3) != 0 {
		t.Errorf("the third job went to the test bot %d times and to production %d times",
			testFake.count(testFake.submits, a3), prodFake.count(prodFake.submits, a3))
	}
}
