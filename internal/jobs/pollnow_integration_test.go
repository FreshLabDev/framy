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

// sleepingQueue is a queue whose runs in the background are asked about every
// ten minutes, so that only PollNowTx can make a poll happen inside a test.
func sleepingQueue(t *testing.T, p *fakeProvider) (*Queue, *pgxpool.Pool) {
	t.Helper()
	pool := testPool(t)
	q, err := NewQueue(pool, Config{
		Providers: map[string]Provider{"openrouter": p},
		Sink:      p,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		PollEvery: 10 * time.Minute,
		StopGrace: 5 * time.Second,
		FetchPoll: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return q, pool
}

func pollNow(t *testing.T, q *Queue, pool *pgxpool.Pool, id int64, commit bool) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	woken, err := q.PollNowTx(ctx, tx, id)
	if err != nil {
		t.Fatal(err)
	}
	if commit {
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return woken
}

func riverState(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	return scalar[string](t, pool, `SELECT r.state::text FROM framy.river_job r JOIN framy.job j ON j.river_job_id = r.id WHERE j.id = $1`, id)
}

func TestPollNowBringsASleepingPollForward(t *testing.T) {
	p := newFake()
	p.submit = func(context.Context, Job) (Submission, error) { return Submission{ProviderJobID: "gen-1"}, nil }
	p.poll = func(context.Context, Job) (Outcome, error) {
		cost := 0.5
		return Outcome{Results: []Result{{ContentType: "video/mp4", Data: []byte("mp4")}}, CostUSD: &cost}, nil
	}
	q, pool := sleepingQueue(t, p)
	id := enqueue(t, q, job(alice, videoOffer))
	start(t, q)
	eventually(t, "the run to sleep until its poll", func() bool {
		return get(t, q, id).ProviderJobID == "gen-1" && riverState(t, pool, id) == "scheduled"
	})

	// A transaction that is rolled back wakes nothing.
	pollNow(t, q, pool, id, false)
	time.Sleep(500 * time.Millisecond)
	if p.count(p.polls, id) != 0 || riverState(t, pool, id) != "scheduled" {
		t.Fatalf("a rolled-back wake polled: %d polls, River state %s", p.count(p.polls, id), riverState(t, pool, id))
	}

	if !pollNow(t, q, pool, id, true) {
		t.Fatal("PollNowTx did not bring the poll forward")
	}
	eventually(t, "the poll that was brought forward", func() bool { return get(t, q, id).Status == Done })
	if p.count(p.polls, id) != 1 || p.count(p.submits, id) != 1 || p.count(p.kept, id) != 1 {
		t.Errorf("%d polls, %d submits, %d kept", p.count(p.polls, id), p.count(p.submits, id), p.count(p.kept, id))
	}
}

func TestPollNowLeavesEverythingElseAlone(t *testing.T) {
	p := newFake()
	p.submit = func(context.Context, Job) (Submission, error) { return Submission{ProviderJobID: "gen-1"}, nil }
	p.poll = func(context.Context, Job) (Outcome, error) { return Outcome{Running: true}, nil }
	q, pool := sleepingQueue(t, p)
	ctx := context.Background()

	if pollNow(t, q, pool, 424242, true) {
		t.Error("a job that does not exist was woken")
	}

	// Queued, never started: there is no run to ask about.
	queued := enqueue(t, q, job(alice, videoOffer))
	if pollNow(t, q, pool, queued, true) {
		t.Error("a queued job was woken")
	}

	start(t, q)
	eventually(t, "the run to sleep", func() bool {
		return get(t, q, queued).ProviderJobID == "gen-1" && riverState(t, pool, queued) == "scheduled"
	})

	// Running, but its provider job id is not saved: the poll that is due finds
	// out; there is nothing to ask about yet.
	if _, err := pool.Exec(ctx, `UPDATE framy.job SET provider_job_id = NULL WHERE id = $1`, queued); err != nil {
		t.Fatal(err)
	}
	if pollNow(t, q, pool, queued, true) {
		t.Error("a run with no provider job id was woken")
	}
	if _, err := pool.Exec(ctx, `UPDATE framy.job SET provider_job_id = 'gen-1' WHERE id = $1`, queued); err != nil {
		t.Fatal(err)
	}

	// Ended: nothing left to bring forward.
	if _, err := pool.Exec(ctx, `UPDATE framy.job SET status = 'failed', finished_at = now() WHERE id = $1`, queued); err != nil {
		t.Fatal(err)
	}
	if pollNow(t, q, pool, queued, true) {
		t.Error("an ended job was woken")
	}
}

// River does not touch a job that is being worked: a webhook that lands in the
// middle of a poll brings nothing forward, and the poll's own turn comes.
func TestPollNowDoesNotTouchARunThatIsBeingPolled(t *testing.T) {
	p := newFake()
	p.submit = func(context.Context, Job) (Submission, error) { return Submission{ProviderJobID: "gen-1"}, nil }
	inPoll, release := make(chan struct{}), make(chan struct{})
	first := true
	p.poll = func(ctx context.Context, _ Job) (Outcome, error) {
		if first {
			first = false
			close(inPoll)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return Outcome{Running: true}, nil
	}
	q, pool := sleepingQueue(t, p)
	id := enqueue(t, q, job(alice, videoOffer))
	start(t, q)
	eventually(t, "the run to sleep", func() bool {
		return get(t, q, id).ProviderJobID == "gen-1" && riverState(t, pool, id) == "scheduled"
	})
	if !pollNow(t, q, pool, id, true) {
		t.Fatal("the first wake did not bring the poll forward")
	}
	<-inPoll
	if pollNow(t, q, pool, id, true) {
		t.Error("a run that is being polled was reported woken")
	}
	close(release)
	eventually(t, "the poll to end and the run to sleep again", func() bool { return riverState(t, pool, id) == "scheduled" })
}
