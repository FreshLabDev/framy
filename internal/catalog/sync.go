// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"context"
	"log/slog"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// Source is where the synced offers come from: OpenRouter, or a fake in
// tests.
type Source interface {
	Fetch(context.Context) (Upstream, error)
}

// Store saves a snapshot and says when the last one was saved.
type Store interface {
	SaveCatalog(context.Context, Snapshot, time.Time) (Saved, error)
	CatalogSyncedAt(context.Context) (time.Time, error)
}

// Saved is what a save changed, by offer id.
type Saved struct {
	Added    []string
	Restored []string
	Disabled []string
	Offers   int
	Models   int
}

// Syncer refreshes the catalog every Every, and after a failure sooner, every
// Retry. A failed sync changes nothing, so the catalog stays as the last good
// hour left it.
type Syncer struct {
	Source Source
	Files  Files
	Store  Store
	Log    *slog.Logger
	Every  time.Duration
	Retry  time.Duration
	Now    func() time.Time
}

// Run syncs until ctx ends. After a restart it waits out what is left of the
// hour since the last sync, so a deploy does not cost an extra round of
// requests.
func (s *Syncer) Run(ctx context.Context) {
	now := s.now()
	wait := time.Duration(0)
	if last, err := s.Store.CatalogSyncedAt(ctx); err != nil {
		s.Log.Warn("catalog sync time unknown; syncing now", "error", err)
	} else if !last.IsZero() {
		wait = max(0, s.Every-now.Sub(last))
	}
	for {
		if !sleep(ctx, wait) {
			return
		}
		wait = s.Every
		if err := s.SyncOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			wait = s.Retry
		}
	}
}

// SyncOnce fetches, builds and saves one snapshot.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	started := s.now()
	up, err := s.Source.Fetch(ctx)
	if err != nil {
		metrics.CatalogSyncFailures.Inc()
		s.Log.Error("catalog fetch failed; catalog unchanged", "error", err)
		return err
	}
	snap := Build(up, s.Files, started)
	for _, w := range snap.Warnings {
		s.Log.Warn("catalog overlay mismatch", "detail", w)
	}
	for _, id := range snap.Keep {
		s.Log.Warn("catalog offer not refreshed; kept as it was", "offer", id)
	}
	saved, err := s.Store.SaveCatalog(ctx, snap, started)
	if err != nil {
		metrics.CatalogSyncFailures.Inc()
		s.Log.Error("catalog save failed; catalog unchanged", "error", err)
		return err
	}
	metrics.CatalogSyncs.Inc()
	s.Log.Info("catalog synced",
		"models", saved.Models, "offers", saved.Offers,
		"added", saved.Added, "restored", saved.Restored, "disabled", saved.Disabled,
		"kept", len(snap.Keep), "warnings", len(snap.Warnings),
		"took", s.now().Sub(started).Round(time.Millisecond).String())
	return nil
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
