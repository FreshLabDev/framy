// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// catalogLockKey keeps two syncs from saving at once during a rolling
// restart.
const catalogLockKey int64 = 0x6672616d7963 // "framyc"

// ErrCatalogShrank refuses a sync that would disable most of what OpenRouter
// listed before. A real exodus is rare; a broken answer is not.
var ErrCatalogShrank = errors.New("catalog sync would disable more than half of the OpenRouter offers")

// SaveCatalog writes a snapshot in one transaction: models and offers are
// upserted, and an offer the snapshot no longer has is disabled, unless the
// snapshot asks to keep it. A model whose offers are all disabled is disabled
// too. Nothing is deleted: jobs will point at these rows.
func (s *Store) SaveCatalog(ctx context.Context, snap catalog.Snapshot, now time.Time) (catalog.Saved, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return catalog.Saved{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, catalogLockKey); err != nil {
		return catalog.Saved{}, fmt.Errorf("catalog lock: %w", err)
	}

	type state struct {
		provider string
		disabled bool
	}
	before := map[string]state{}
	rows, err := tx.Query(ctx, `SELECT id, provider, disabled_at IS NOT NULL FROM catalog_offer`)
	if err != nil {
		return catalog.Saved{}, err
	}
	for rows.Next() {
		var id string
		var st state
		if err := rows.Scan(&id, &st.provider, &st.disabled); err != nil {
			rows.Close()
			return catalog.Saved{}, err
		}
		before[id] = st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return catalog.Saved{}, err
	}

	offers := snap.Offers()
	present := map[string]bool{}
	for _, of := range offers {
		present[of.ID] = true
	}
	for _, id := range snap.Keep {
		present[id] = true
	}
	var saved catalog.Saved
	enabledOR, goneOR := 0, 0
	for id, st := range before {
		switch {
		case !present[id] && !st.disabled:
			saved.Disabled = append(saved.Disabled, id)
			if st.provider == catalog.OpenRouter {
				goneOR++
			}
		case present[id] && st.disabled:
			saved.Restored = append(saved.Restored, id)
		}
		if st.provider == catalog.OpenRouter && !st.disabled {
			enabledOR++
		}
	}
	if enabledOR >= 10 && goneOR*2 > enabledOR {
		return catalog.Saved{}, fmt.Errorf("%w: %d of %d", ErrCatalogShrank, goneOR, enabledOR)
	}
	for _, of := range offers {
		if _, ok := before[of.ID]; !ok {
			saved.Added = append(saved.Added, of.ID)
		}
	}

	batch := &pgx.Batch{}
	for _, m := range snap.Models {
		traits, err := json.Marshal(m.Traits)
		if err != nil {
			return catalog.Saved{}, err
		}
		batch.Queue(`
			INSERT INTO catalog_model (id, kind, name, maker, description, featured, free, traits, hidden, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
			ON CONFLICT (id) DO UPDATE SET
				kind = EXCLUDED.kind, name = EXCLUDED.name, maker = EXCLUDED.maker,
				description = EXCLUDED.description, featured = EXCLUDED.featured, free = EXCLUDED.free,
				traits = EXCLUDED.traits, hidden = EXCLUDED.hidden,
				updated_at = EXCLUDED.updated_at, disabled_at = NULL
			WHERE (catalog_model.kind, catalog_model.name, catalog_model.maker, catalog_model.description,
			       catalog_model.featured, catalog_model.free, catalog_model.traits, catalog_model.hidden,
			       catalog_model.disabled_at)
			  IS DISTINCT FROM (EXCLUDED.kind, EXCLUDED.name, EXCLUDED.maker, EXCLUDED.description,
			       EXCLUDED.featured, EXCLUDED.free, EXCLUDED.traits, EXCLUDED.hidden, NULL::timestamptz)`,
			m.ID, string(m.Kind), m.Name, m.Maker, m.Description, m.Featured, m.Free, traits, nullString(m.Hidden), now)
	}
	for _, of := range offers {
		params, err := json.Marshal(nonNil(of.Params))
		if err != nil {
			return catalog.Saved{}, err
		}
		routes, err := json.Marshal(nonNilSlice(of.Routes))
		if err != nil {
			return catalog.Saved{}, err
		}
		var expires any
		if of.ExpiresOn != nil {
			expires = of.ExpiresOn.Format(time.DateOnly)
		}
		batch.Queue(`
			INSERT INTO catalog_offer (id, model_id, provider, provider_model_id, name, input_modalities,
			                           params, routes, expires_on, hidden, fetched_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, $11, $11, $11)
			ON CONFLICT (id) DO UPDATE SET
				model_id = EXCLUDED.model_id, name = EXCLUDED.name, input_modalities = EXCLUDED.input_modalities,
				params = EXCLUDED.params, routes = EXCLUDED.routes, expires_on = EXCLUDED.expires_on,
				hidden = EXCLUDED.hidden, fetched_at = EXCLUDED.fetched_at, disabled_at = NULL,
				updated_at = CASE
					WHEN (catalog_offer.model_id, catalog_offer.name, catalog_offer.input_modalities,
					      catalog_offer.params, catalog_offer.routes, catalog_offer.expires_on,
					      catalog_offer.hidden, catalog_offer.disabled_at)
					  IS DISTINCT FROM (EXCLUDED.model_id, EXCLUDED.name, EXCLUDED.input_modalities,
					      EXCLUDED.params, EXCLUDED.routes, EXCLUDED.expires_on, EXCLUDED.hidden, NULL::timestamptz)
					THEN EXCLUDED.updated_at ELSE catalog_offer.updated_at END`,
			of.ID, of.ModelID, of.Provider, of.ProviderModelID, of.Name, nonNilSlice(of.InputModalities),
			params, routes, expires, nullString(of.Hidden), now)
	}
	if len(saved.Disabled) > 0 {
		batch.Queue(`UPDATE catalog_offer SET disabled_at = $1, updated_at = $1 WHERE id = ANY($2)`, now, saved.Disabled)
	}
	batch.Queue(`
		UPDATE catalog_model m SET disabled_at = $1, updated_at = $1
		WHERE disabled_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM catalog_offer o WHERE o.model_id = m.id AND o.disabled_at IS NULL)`, now)
	batch.Queue(`UPDATE runtime_state SET catalog_synced_at = $1, updated_at = now() WHERE singleton`, now)
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return catalog.Saved{}, fmt.Errorf("save catalog: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Saved{}, err
	}
	for _, list := range [][]string{saved.Added, saved.Restored, saved.Disabled} {
		slices.Sort(list)
	}
	saved.Models, saved.Offers = len(snap.Models), len(offers)
	return saved, nil
}

// CatalogSyncedAt is when the last snapshot was saved, zero before the first.
func (s *Store) CatalogSyncedAt(ctx context.Context) (time.Time, error) {
	var at *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT catalog_synced_at FROM runtime_state WHERE singleton`).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if at == nil {
		return time.Time{}, nil
	}
	return *at, nil
}

// TypicalRuns is how many runs of an offer in TypicalWindow it takes to say
// how long one typically takes.
const (
	TypicalRuns   = 5
	TypicalWindow = 30 * 24 * time.Hour
)

// TypicalSeconds is how long a run of each offer typically takes, from the
// press of Generate to the result: the median of its runs that ended done in
// TypicalWindow, for an offer with at least TypicalRuns of them, in whole
// seconds rounded up. The site shows it beside the time a run has waited.
func (s *Store) TypicalSeconds(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT offer_id, ceil(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - queued_at)))::int
		FROM job
		WHERE status = 'done' AND queued_at IS NOT NULL AND finished_at > now() - make_interval(secs => $1)
		GROUP BY offer_id
		HAVING count(*) >= $2`, TypicalWindow.Seconds(), TypicalRuns)
	if err != nil {
		return nil, fmt.Errorf("typical seconds: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var offer string
		var secs int
		if err := rows.Scan(&offer, &secs); err != nil {
			return nil, fmt.Errorf("typical seconds: %w", err)
		}
		out[offer] = max(secs, 1)
	}
	return out, rows.Err()
}

// Catalog returns what the picker shows: models that are on and not hidden,
// featured first, each with its offers that are on and not hidden.
func (s *Store) Catalog(ctx context.Context) ([]catalog.Model, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.kind, m.name, m.maker, m.description, m.featured, m.free, m.traits,
		       o.id, o.provider, o.provider_model_id, o.name, o.input_modalities, o.params, o.routes, o.expires_on
		FROM catalog_model m
		JOIN catalog_offer o ON o.model_id = m.id AND o.disabled_at IS NULL AND o.hidden IS NULL
		WHERE m.disabled_at IS NULL AND m.hidden IS NULL
		ORDER BY m.featured DESC, m.kind, m.name, m.id, o.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catalog.Model
	for rows.Next() {
		var (
			m              catalog.Model
			of             catalog.Offer
			kind           string
			traits         []byte
			params, routes []byte
			expires        *time.Time
		)
		if err := rows.Scan(&m.ID, &kind, &m.Name, &m.Maker, &m.Description, &m.Featured, &m.Free, &traits,
			&of.ID, &of.Provider, &of.ProviderModelID, &of.Name, &of.InputModalities, &params, &routes, &expires); err != nil {
			return nil, err
		}
		m.Kind, of.Kind, of.ModelID, of.ExpiresOn = catalog.Kind(kind), catalog.Kind(kind), m.ID, expires
		if err := json.Unmarshal(traits, &m.Traits); err != nil {
			return nil, fmt.Errorf("model %s traits: %w", m.ID, err)
		}
		if err := json.Unmarshal(params, &of.Params); err != nil {
			return nil, fmt.Errorf("offer %s params: %w", of.ID, err)
		}
		if err := json.Unmarshal(routes, &of.Routes); err != nil {
			return nil, fmt.Errorf("offer %s routes: %w", of.ID, err)
		}
		if n := len(out); n > 0 && out[n-1].ID == m.ID {
			out[n-1].Offers = append(out[n-1].Offers, of)
			continue
		}
		m.Offers = []catalog.Offer{of}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nonNil[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
