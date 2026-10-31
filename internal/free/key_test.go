// SPDX-License-Identifier: Apache-2.0
package free

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTheKeyWatchKeepsTheLastReadingUntilItIsTooOld(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	reading := KeyState{Limited: true, RemainingUSD: 1.5, Daily: true}
	var fail error
	w := NewKeyWatch(func(context.Context) (KeyState, error) { return reading, fail }, time.Minute, nil)
	w.now = func() time.Time { return now }

	if _, ok := w.State(); ok {
		t.Fatal("a reading before the first read")
	}
	if err := w.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, ok := w.State(); !ok || st != reading {
		t.Fatalf("state = %+v, %v", st, ok)
	}

	// OpenRouter failing leaves the last reading, for a while.
	fail = errors.New("openrouter is down")
	if err := w.Refresh(context.Background()); err == nil {
		t.Fatal("the failure was swallowed")
	}
	now = now.Add(4 * time.Minute)
	if st, ok := w.State(); !ok || st != reading {
		t.Fatalf("after 4 minutes: %+v, %v", st, ok)
	}
	// Then it is no reading: the allowance goes by the budget and by the
	// money failures alone.
	now = now.Add(2 * time.Minute)
	if _, ok := w.State(); ok {
		t.Fatal("a reading six minutes old was trusted")
	}
}

func TestTheKeyWatchReadsAtStartAndKeepsReading(t *testing.T) {
	var reads atomic.Int32
	w := NewKeyWatch(func(context.Context) (KeyState, error) {
		reads.Add(1)
		return KeyState{}, nil
	}, 10*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for reads.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d reads", reads.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := w.State(); !ok {
		t.Fatal("no reading after reads")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}
