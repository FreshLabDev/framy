// SPDX-License-Identifier: Apache-2.0
//go:build integration

package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/price"
)

// spyAdmitter records what it is asked and answers as told.
type spyAdmitter struct {
	mu     sync.Mutex
	asked  []Admission
	answer error
}

func (s *spyAdmitter) Admit(ctx context.Context, tx pgx.Tx, a Admission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, a)
	// A write made while asking goes or stays with the job's own transaction.
	if _, err := tx.Exec(ctx, `UPDATE framy.runtime_state SET telegram_offset = telegram_offset + 1`); err != nil {
		return err
	}
	return s.answer
}

func admitQueue(t *testing.T, a Admitter) *Queue {
	t.Helper()
	q, err := NewQueue(testPool(t), Config{Admit: a, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestAFreeJobIsAskedAboutInItsOwnTransaction(t *testing.T) {
	spy := &spyAdmitter{}
	q := admitQueue(t, spy)
	ctx := context.Background()
	n := job(alice, imageOffer)
	n.Free = true
	n.Settings = map[string]any{"n": 1}
	n.Estimate = &price.Estimate{USD: 0.0195}
	n.DedupeKey = TelegramUpdate(7)
	pool := testPool(t)
	offset := scalar[int](t, pool, `SELECT telegram_offset FROM framy.runtime_state`)
	if _, _, err := q.Enqueue(ctx, n); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, pool, `SELECT telegram_offset FROM framy.runtime_state`); got != offset+1 {
		t.Fatalf("the write made while asking = %d, want %d", got, offset+1)
	}
	want := Admission{UserID: alice, DedupeKey: "tg:7", Offer: imageOffer.ID, Kind: imageOffer.Kind, Settings: map[string]any{"n": 1}, EstimateUSD: 0.0195}
	if len(spy.asked) != 1 || spy.asked[0].UserID != want.UserID || spy.asked[0].DedupeKey != want.DedupeKey || spy.asked[0].Offer != want.Offer ||
		spy.asked[0].Kind != want.Kind || spy.asked[0].EstimateUSD != want.EstimateUSD || spy.asked[0].Inputs != 0 || spy.asked[0].Settings["n"] != 1 {
		t.Fatalf("asked %+v, want %+v", spy.asked, want)
	}

	// A draft is asked about when made, and again when queued, this time as
	// it was stored.
	n.DedupeKey = TelegramUpdate(8)
	draft, _, err := q.Draft(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Generate(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if len(spy.asked) != 3 {
		t.Fatalf("asked %d times, want 3: the run, the draft, the queued draft", len(spy.asked))
	}
	queued := spy.asked[2]
	if queued.UserID != alice || queued.DedupeKey != "" || queued.Offer != imageOffer.ID || queued.Kind != imageOffer.Kind ||
		queued.EstimateUSD != 0.0195 || queued.Settings["n"] != float64(1) || queued.Inputs != 0 {
		t.Fatalf("the queued draft was asked as %+v", queued)
	}

	// A run on the person's own key is not asked about, and a second press
	// on a draft that was queued is refused as ever, not asked about.
	own := job(bob, imageOffer)
	if _, _, err := q.Enqueue(ctx, own); err != nil {
		t.Fatal(err)
	}
	if err := q.Generate(ctx, draft); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("second press = %v", err)
	}
	if len(spy.asked) != 3 {
		t.Fatalf("asked %d times after a run on the person's key and a second press, want still 3", len(spy.asked))
	}
}

func TestARefusedFreeJobLeavesNothingBehind(t *testing.T) {
	refusal := errors.New("the allowance says no")
	spy := &spyAdmitter{answer: refusal}
	q := admitQueue(t, spy)
	pool := testPool(t)
	ctx := context.Background()
	offset := scalar[int](t, pool, `SELECT telegram_offset FROM framy.runtime_state`)
	n := job(alice, imageOffer)
	n.Free = true
	if _, created, err := q.Enqueue(ctx, n); !errors.Is(err, refusal) || created {
		t.Fatalf("Enqueue = %v %v; the refusal comes back as it is", created, err)
	}
	if _, _, err := q.Draft(ctx, n); !errors.Is(err, refusal) {
		t.Fatalf("Draft = %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.job`); n != 0 {
		t.Fatalf("%d jobs", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job`); n != 0 {
		t.Fatalf("%d River jobs", n)
	}
	if got := scalar[int](t, pool, `SELECT telegram_offset FROM framy.runtime_state`); got != offset {
		t.Fatalf("a write made while the allowance was asked survived the refusal: %d, was %d", got, offset)
	}

	// A draft made while the answer was yes stays a draft when the answer
	// turns to no, and queues once it turns back.
	spy.answer = nil
	draft, _, err := q.Draft(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	spy.answer = refusal
	if err := q.Generate(ctx, draft); !errors.Is(err, refusal) {
		t.Fatalf("Generate = %v", err)
	}
	if got := scalar[string](t, pool, `SELECT status FROM framy.job WHERE id = $1`, draft); got != string(Draft) {
		t.Fatalf("status = %s", got)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job`); n != 0 {
		t.Fatalf("%d River jobs", n)
	}
	spy.answer = nil
	if err := q.Generate(ctx, draft); err != nil {
		t.Fatal(err)
	}
}
