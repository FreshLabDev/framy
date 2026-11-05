// SPDX-License-Identifier: Apache-2.0
//go:build integration

package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Every way a person's job comes to be, created, queued from a draft or
// pressed as a batch, waits while their account is being deleted, which
// holds LockPerson for its transaction. A job is then seen by the deletion
// or made after it, never in between with its files gone and its prompt
// kept. Another person's jobs do not wait.
func TestAJobWaitsWhileItsPersonIsBeingDeleted(t *testing.T) {
	q, pool := testQueue(t, newFake())
	ctx := context.Background()
	draft := job(alice, imageOffer)
	draft.DedupeKey = "tg:draft"
	draftID, _, err := q.Draft(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := LockPerson(ctx, tx, alice); err != nil {
		t.Fatal(err)
	}

	if _, _, err := q.Enqueue(ctx, job(bob, imageOffer)); err != nil {
		t.Fatalf("another person's job: %v", err)
	}

	ways := map[string]func() error{
		"enqueue": func() error { _, _, err := q.Enqueue(ctx, job(alice, imageOffer)); return err },
		"batch": func() error {
			ns := []New{job(alice, imageOffer), job(alice, imageOffer)}
			for i := range ns {
				ns[i].Batch, ns[i].DedupeKey = "b", fmt.Sprintf("web:b#%d", i)
			}
			_, _, err := q.EnqueueBatch(ctx, ns)
			return err
		},
		"draft":    func() error { _, _, err := q.Draft(ctx, job(alice, imageOffer)); return err },
		"generate": func() error { return q.Generate(ctx, draftID) },
	}
	ended := make(chan string, len(ways))
	for name, way := range ways {
		go func() {
			if err := way(); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			ended <- name
		}()
	}
	select {
	case name := <-ended:
		t.Fatalf("%s went ahead while the person was locked", name)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range ways {
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("a job still waits after the lock went")
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.job WHERE user_id = $1 AND status = 'queued'`, alice); n != 4 {
		t.Errorf("%d of alice's jobs queued, want 4", n)
	}
}

// A batch is one person's press: one with another person's job in it is a
// mistake, refused before anything is written.
func TestABatchIsOnePersonsPress(t *testing.T) {
	q, pool := testQueue(t, newFake())
	_, _, err := q.EnqueueBatch(context.Background(), []New{job(alice, imageOffer), job(bob, imageOffer)})
	if err == nil {
		t.Fatal("a batch of two people's jobs was queued")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.job`); n != 0 {
		t.Errorf("%d jobs", n)
	}
}
