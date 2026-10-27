// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoFlow: the store holds no flow for this state: it was never made, was
// already spent, was replaced by a newer start, or was swept after it
// expired.
var ErrNoFlow = errors.New("no such OAuth flow")

// Flow is one attempt in progress, from the redirect to the provider until
// its callback.
type Flow struct {
	// State is the hash of the state the browser holds (hashState).
	State [sha256.Size]byte
	// UserID is the person the flow was started for.
	UserID   int64
	Provider string
	// Verifier is the PKCE code_verifier. It is a secret of this flow: it is
	// sent to the provider once, with the code, and nowhere else.
	Verifier string
	// ExpiresAt is the end of the flow's life. Take returns an expired flow
	// as it finds it, so the caller can tell a late callback from an unknown
	// one; deciding is the caller's.
	ExpiresAt time.Time
}

// Store keeps the flows in progress.
type Store interface {
	// Put keeps the flow in place of the person's earlier flow for the same
	// provider, and forgets every flow that had expired by now.
	Put(ctx context.Context, f Flow, now time.Time) error
	// Take removes the flow and returns it. Of any number of concurrent
	// Takes of one state exactly one gets the flow; the rest get ErrNoFlow.
	Take(ctx context.Context, state [sha256.Size]byte) (Flow, error)
}

// PGStore keeps the flows in the oauth_state table (migration 011).
type PGStore struct{ pool *pgxpool.Pool }

// NewPGStore builds the store on pool, whose search_path is framy.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Put(ctx context.Context, f Flow, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM oauth_state WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("sweep the OAuth flows: %w", err)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_state (state_hash, user_id, provider, verifier, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, provider) DO UPDATE SET
		  state_hash = EXCLUDED.state_hash, verifier = EXCLUDED.verifier, expires_at = EXCLUDED.expires_at`,
		f.State[:], f.UserID, f.Provider, f.Verifier, f.ExpiresAt)
	if err != nil {
		return fmt.Errorf("keep the OAuth flow: %w", err)
	}
	return nil
}

func (s *PGStore) Take(ctx context.Context, state [sha256.Size]byte) (Flow, error) {
	f := Flow{State: state}
	err := s.pool.QueryRow(ctx, `
		DELETE FROM oauth_state WHERE state_hash = $1
		RETURNING user_id, provider, verifier, expires_at`, state[:],
	).Scan(&f.UserID, &f.Provider, &f.Verifier, &f.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Flow{}, ErrNoFlow
	}
	if err != nil {
		return Flow{}, fmt.Errorf("take the OAuth flow: %w", err)
	}
	return f, nil
}
