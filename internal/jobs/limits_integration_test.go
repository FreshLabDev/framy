// SPDX-License-Identifier: Apache-2.0
//go:build integration

package jobs

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// deadlines records how long each step of a job was given.
type deadlines struct {
	mu   sync.Mutex
	left map[string]time.Duration
}

func (d *deadlines) note(step string, ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := ctx.Deadline(); ok {
		d.left[step] = time.Until(at)
	} else {
		d.left[step] = -1
	}
}

func (d *deadlines) get(step string) time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.left[step]
}

// A video's last poll downloads the video and the step after it copies it to
// R2, so both get minutes; an image keeps the design's limits.
func TestAVideoHasTimeToBeDownloadedAndKept(t *testing.T) {
	d := &deadlines{left: map[string]time.Duration{}}
	f := newFake()
	f.submit = func(ctx context.Context, j Job) (Submission, error) {
		d.note(string(j.Kind)+" submit", ctx)
		if j.Kind == "video" {
			return Submission{ProviderJobID: "vid-1"}, nil
		}
		return done(0.02), nil
	}
	f.poll = func(ctx context.Context, j Job) (Outcome, error) {
		d.note(string(j.Kind)+" poll", ctx)
		cost := 0.3
		return Outcome{Results: []Result{{ContentType: "video/mp4", Data: []byte("mp4")}}, CostUSD: &cost}, nil
	}
	f.onKeep = func(ctx context.Context, j Job) error { d.note(string(j.Kind)+" keep", ctx); return nil }
	q, pool := testQueue(t, f)
	start(t, q)

	video := enqueue(t, q, job(alice, videoOffer))
	image := enqueue(t, q, job(bob, imageOffer))
	eventually(t, "both jobs done", func() bool {
		return get(t, q, video).Status == Done && get(t, q, image).Status == Done
	})

	for _, c := range []struct {
		step     string
		from, to time.Duration
	}{
		{"video submit", 50 * time.Second, videoCall},
		{"video poll", videoPoll - 10*time.Second, videoPoll},
		{"video keep", videoKeep - 10*time.Second, videoKeep},
		{"image submit", imageCall - 10*time.Second, imageCall},
		{"image keep", settleWrites - 10*time.Second, settleWrites},
	} {
		if got := d.get(c.step); got < c.from || got > c.to {
			t.Errorf("%s had %v, want %v to %v", c.step, got, c.from, c.to)
		}
	}
	// The flag that gives the attempt its limit is on the River job.
	if v := scalar[*bool](t, pool, `SELECT (args->>'video')::bool FROM framy.river_job WHERE (args->>'job_id')::bigint = $1`, video); v == nil || !*v {
		t.Errorf("the video's River job is not marked: %v", v)
	}
	if v := scalar[*bool](t, pool, `SELECT (args->>'video')::bool FROM framy.river_job WHERE (args->>'job_id')::bigint = $1`, image); v != nil {
		t.Errorf("the image's River job is marked: %v", *v)
	}
}

// A copy that takes all of its time still leaves the ending its own: the
// paid picture is done, or failed as storage with its cost, and never left
// for the next attempt to call interrupted.
func TestASlowCopyLeavesTheEndingItsOwnTime(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		kept   bool
		status Status
		class  Class
	}{
		{"kept as its time ran out", true, Done, ""},
		{"not kept in time", false, Failed, Storage},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.onKeep = func(ctx context.Context, _ Job) error {
				<-ctx.Done()
				if c.kept {
					return nil
				}
				return fmt.Errorf("put the result: %w", ctx.Err())
			}
			q, _ := testQueue(t, f)
			q.keepFor = func(Job, []Result) time.Duration { return 200 * time.Millisecond }
			id := enqueue(t, q, job(alice, imageOffer))
			if err := q.work(ctx, id); err != nil {
				t.Fatalf("the attempt: %v", err)
			}
			j := get(t, q, id)
			if j.Status != c.status || j.ErrorClass != c.class || j.ActualUSD == nil || *j.ActualUSD != 0.02 {
				t.Fatalf("%s %s, cost %v; want %s %s with the cost", j.Status, j.ErrorClass, j.ActualUSD, c.status, c.class)
			}
		})
	}
}

// What the provider charged is written down before the results are copied,
// and an attempt that comes after one which kept them and stopped short of
// the ending finishes the job as done: the person sees the paid pictures,
// not "interrupted".
func TestResultsKeptBeforeAStopMakeTheRunDone(t *testing.T) {
	ctx := context.Background()
	t.Run("the cost is written before the copy", func(t *testing.T) {
		f := newFake()
		q, pool := testQueue(t, f)
		var noted *float64
		f.onKeep = func(ctx context.Context, j Job) error {
			return pool.QueryRow(ctx, `SELECT actual_usd::float8 FROM framy.job WHERE id=$1`, j.ID).Scan(&noted)
		}
		id := enqueue(t, q, job(alice, imageOffer))
		if err := q.work(ctx, id); err != nil {
			t.Fatal(err)
		}
		if noted == nil || *noted != 0.02 {
			t.Fatalf("the cost while the results were copied: %v", noted)
		}
	})
	t.Run("a later attempt finds the results", func(t *testing.T) {
		f := newFake()
		q, pool := testQueue(t, f)
		id := enqueue(t, q, job(alice, imageOffer))
		for _, stmt := range []string{
			`UPDATE framy.job SET status='running', started_at=now(), submitted_at=now(), submits=1, actual_usd=0.02 WHERE id=$1`,
			`INSERT INTO framy.asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
			 VALUES (1001, 'result', $1::bigint, 'u/1001/out/' || $1::bigint::text || '-0.png', 'image/png', 3, 'x')`,
		} {
			if _, err := pool.Exec(ctx, stmt, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := q.work(ctx, id); err != nil {
			t.Fatal(err)
		}
		j := get(t, q, id)
		results := scalar[int](t, pool, `SELECT results FROM framy.job WHERE id=$1`, id)
		if j.Status != Done || results != 1 || j.ActualUSD == nil || *j.ActualUSD != 0.02 || f.count(f.submits, id) != 0 {
			t.Fatalf("%s %s, %d results, cost %v, sent %d more times", j.Status, j.ErrorClass, results, j.ActualUSD, f.count(f.submits, id))
		}
	})
}
