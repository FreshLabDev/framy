// SPDX-License-Identifier: Apache-2.0
//go:build integration

package jobs

import (
	"context"
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
	f.onKeep = func(ctx context.Context, j Job) { d.note(string(j.Kind)+" keep", ctx) }
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
