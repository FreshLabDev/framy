// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	up    Upstream
	err   error
	calls chan struct{}
}

func (f *fakeSource) Fetch(context.Context) (Upstream, error) {
	if f.calls != nil {
		f.calls <- struct{}{}
	}
	return f.up, f.err
}

type fakeStore struct {
	mu     sync.Mutex
	last   time.Time
	saves  []Snapshot
	saveAt []time.Time
	err    error
}

func (f *fakeStore) SaveCatalog(_ context.Context, s Snapshot, now time.Time) (Saved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Saved{}, f.err
	}
	f.saves = append(f.saves, s)
	f.saveAt = append(f.saveAt, now)
	return Saved{Models: len(s.Models), Offers: len(s.Offers())}, nil
}

func (f *fakeStore) CatalogSyncedAt(context.Context) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSyncOnceSavesTheBuiltSnapshot(t *testing.T) {
	store := &fakeStore{}
	s := &Syncer{
		Source: &fakeSource{up: fixtureUpstream(t)},
		Files:  embeddedFiles(t),
		Store:  store,
		Log:    quiet(),
		Now:    func() time.Time { return fixtureDay },
	}
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.saves) != 1 || !store.saveAt[0].Equal(fixtureDay) {
		t.Fatalf("saves = %d at %v", len(store.saves), store.saveAt)
	}
	nb := modelByID(t, store.saves[0], "google/gemini-nano-banana-2.1")
	if len(nb.Offers) != 2 {
		t.Fatalf("nano banana offers = %v", offerIDs(nb))
	}
}

func TestSyncOnceSavesNothingAfterAFailedFetch(t *testing.T) {
	store := &fakeStore{}
	s := &Syncer{Source: &fakeSource{err: errors.New("down")}, Files: embeddedFiles(t), Store: store, Log: quiet()}
	if err := s.SyncOnce(context.Background()); err == nil {
		t.Fatal("a failed fetch passed")
	}
	if len(store.saves) != 0 {
		t.Fatal("a failed fetch was saved")
	}
}

func TestRunSyncsAtOnceWithoutAPastSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &fakeSource{err: errors.New("down"), calls: make(chan struct{}, 4)}
	s := &Syncer{Source: src, Files: embeddedFiles(t), Store: &fakeStore{}, Log: quiet(), Every: time.Hour, Retry: time.Hour}
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-src.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("no sync at start")
	}
	cancel()
	<-done
}

func TestRunWaitsOutTheHourAfterARestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &fakeSource{calls: make(chan struct{}, 4)}
	now := time.Now()
	s := &Syncer{Source: src, Files: embeddedFiles(t), Store: &fakeStore{last: now.Add(-10 * time.Minute)}, Log: quiet(), Every: time.Hour, Retry: time.Hour}
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-src.calls:
		t.Fatal("synced again ten minutes after the last sync")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	<-done
}
