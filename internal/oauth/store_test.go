// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemStoreKeepsTheStoreContract(t *testing.T) {
	testStoreContract(t, func(t *testing.T) Store { return newMemStore() })
}

// testStoreContract is what every Store promises; the database store runs it
// too (integration_test.go). The people it uses, ann and bob, have to exist
// where the store needs them to.
func testStoreContract(t *testing.T, newStore func(t *testing.T) Store) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	flow := func(user int64, state, verifier string, ttl time.Duration) Flow {
		return Flow{State: hashState(state), UserID: user, Provider: Provider, Verifier: verifier, ExpiresAt: now.Add(ttl)}
	}
	same := func(t *testing.T, got, want Flow) {
		t.Helper()
		if got.State != want.State || got.UserID != want.UserID || got.Provider != want.Provider ||
			got.Verifier != want.Verifier || !got.ExpiresAt.Equal(want.ExpiresAt) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	}

	t.Run("a flow is taken once", func(t *testing.T) {
		s := newStore(t)
		f := flow(ann, "state-1", "verifier-1", 10*time.Minute)
		if err := s.Put(ctx, f, now); err != nil {
			t.Fatal(err)
		}
		got, err := s.Take(ctx, f.State)
		if err != nil {
			t.Fatal(err)
		}
		same(t, got, f)
		if _, err := s.Take(ctx, f.State); !errors.Is(err, ErrNoFlow) {
			t.Errorf("the second Take: %v, want ErrNoFlow", err)
		}
	})

	t.Run("an unknown state is no flow", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Take(ctx, hashState("never put")); !errors.Is(err, ErrNoFlow) {
			t.Errorf("Take: %v, want ErrNoFlow", err)
		}
	})

	t.Run("a person has one flow, people have their own", func(t *testing.T) {
		s := newStore(t)
		first, second := flow(ann, "state-a1", "verifier-a1", time.Minute), flow(ann, "state-a2", "verifier-a2", time.Minute)
		other := flow(bob, "state-b1", "verifier-b1", time.Minute)
		for _, f := range []Flow{first, other, second} {
			if err := s.Put(ctx, f, now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Take(ctx, first.State); !errors.Is(err, ErrNoFlow) {
			t.Errorf("the replaced flow: %v, want ErrNoFlow", err)
		}
		for _, f := range []Flow{second, other} {
			got, err := s.Take(ctx, f.State)
			if err != nil {
				t.Fatalf("Take: %v", err)
			}
			same(t, got, f)
		}
	})

	t.Run("a Put forgets the flows that have expired", func(t *testing.T) {
		s := newStore(t)
		stale := flow(bob, "state-stale", "verifier-stale", -time.Second)
		if err := s.Put(ctx, stale, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, flow(ann, "state-fresh", "verifier-fresh", time.Minute), now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Take(ctx, stale.State); !errors.Is(err, ErrNoFlow) {
			t.Errorf("the expired flow: %v, want ErrNoFlow", err)
		}
	})

	t.Run("of many Takes at once one gets the flow", func(t *testing.T) {
		s := newStore(t)
		f := flow(ann, "state-race", "verifier-race", time.Minute)
		if err := s.Put(ctx, f, now); err != nil {
			t.Fatal(err)
		}
		const n = 24
		var wg sync.WaitGroup
		var mu sync.Mutex
		won, lost := 0, 0
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.Take(ctx, f.State)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					won++
				case errors.Is(err, ErrNoFlow):
					lost++
				default:
					t.Errorf("Take: %v", err)
				}
			}()
		}
		wg.Wait()
		if won != 1 || lost != n-1 {
			t.Errorf("%d won and %d lost, want 1 and %d", won, lost, n-1)
		}
	})
}
