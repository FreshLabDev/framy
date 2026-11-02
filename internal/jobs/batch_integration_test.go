// SPDX-License-Identifier: Apache-2.0
//go:build integration

package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// refuseKey admits every job but the one whose dedupe key ends with suffix.
type refuseKey struct{ suffix string }

var errRefusedFrame = errors.New("the allowance says no to this frame")

func (r refuseKey) Admit(_ context.Context, _ pgx.Tx, a Admission) error {
	if strings.HasSuffix(a.DedupeKey, r.suffix) {
		return errRefusedFrame
	}
	return nil
}

func press(user int64, batch string, n int) []New {
	ns := make([]New, n)
	for i := range ns {
		ns[i] = job(user, imageOffer)
		ns[i].Free = true
		ns[i].DedupeKey = fmt.Sprintf("web:%d:%s#%d", user, batch, i)
		ns[i].Batch = batch
		ns[i].BatchFrames = []int32{int32(i)}
	}
	ns[0].Settings = map[string]any{"n": 2}
	ns[0].BatchFrames = []int32{0, 3}
	return ns
}

func TestAPressIsQueuedWholeOrNotAtAll(t *testing.T) {
	q := admitQueue(t, refuseKey{suffix: "#2"})
	pool := testPool(t)
	ctx := context.Background()

	// The third job is refused: none of the press stays.
	if _, created, err := q.EnqueueBatch(ctx, press(alice, "refused", 3)); !errors.Is(err, errRefusedFrame) || created {
		t.Fatalf("created %v, %v", created, err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.job`); n != 0 {
		t.Fatalf("%d jobs", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job`); n != 0 {
		t.Fatalf("%d River jobs", n)
	}

	ns := press(alice, "taken", 2)
	ids, created, err := q.EnqueueBatch(ctx, ns)
	if err != nil || !created || len(ids) != 2 {
		t.Fatalf("%v %v %v", ids, created, err)
	}
	if got := scalar[string](t, pool, `SELECT string_agg(batch || ':' || array_to_string(batch_frames, ','), ' ' ORDER BY id) FROM framy.job`); got != "taken:0,3 taken:1" {
		t.Errorf("rows %q", got)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job WHERE kind='framy_generate'`); n != 2 {
		t.Errorf("%d River jobs", n)
	}

	// The same press again answers its jobs and starts nothing.
	again, created, err := q.EnqueueBatch(ctx, ns)
	if err != nil || created || fmt.Sprint(again) != fmt.Sprint(ids) {
		t.Fatalf("again %v %v %v, want %v", again, created, err, ids)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job WHERE kind='framy_generate'`); n != 2 {
		t.Errorf("%d River jobs after the repeat", n)
	}

	// A job of its own keeps no batch.
	one := job(alice, imageOffer)
	if _, _, err := q.Enqueue(ctx, one); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.job WHERE batch IS NULL AND batch_frames IS NULL`); n != 1 {
		t.Errorf("%d jobs without a batch", n)
	}
}
