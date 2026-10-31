// SPDX-License-Identifier: Apache-2.0

//go:build integration

package jobs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// sample is the value of one series of the metrics, 0 when it has none.
func sample(t *testing.T, series string) float64 {
	t.Helper()
	var sb strings.Builder
	metrics.Write(&sb)
	for _, line := range strings.Split(sb.String(), "\n") {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatalf("%s: %v", line, err)
			}
			return x
		}
	}
	return 0
}

// A job that ends goes into the metrics: what it cost and who paid, how long
// it took when it made something, and why it made nothing otherwise.
func TestAnEndedJobIsCounted(t *testing.T) {
	ctx := context.Background()
	cost := `framy_job_cost_usd_total{provider="openrouter",model="test/image",payer="key"}`
	took := `framy_job_duration_seconds_count{kind="image"}`
	storage := `framy_job_failures_total{reason="storage"}`
	cost0, took0, storage0 := sample(t, cost), sample(t, took), sample(t, storage)

	p := newFake()
	p.submit = func(context.Context, Job) (Submission, error) { return done(0.0201), nil }
	q, _ := testQueue(t, p)
	id := enqueue(t, q, job(alice, imageOffer))
	if waiting, err := q.Waiting(ctx); err != nil || waiting["queued"] < 1 || waiting["running"] != 0 {
		t.Fatalf("waiting %v, %v", waiting, err)
	}
	if err := q.work(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := sample(t, cost) - cost0; got < 0.02009 || got > 0.02011 {
		t.Fatalf("cost counted %v", got)
	}
	if got := sample(t, took) - took0; got != 1 {
		t.Fatalf("durations counted %v", got)
	}

	q.cfg.Inputs = &fakeInputs{err: &Error{Class: Storage, Err: errors.New("the photo was deleted")}}
	id = enqueue(t, q, job(alice, imageOffer))
	if err := q.work(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := sample(t, storage) - storage0; got != 1 {
		t.Fatalf("failures counted %v", got)
	}
	if got := sample(t, took) - took0; got != 1 {
		t.Fatalf("a failure counted as a duration: %v", got)
	}
}
