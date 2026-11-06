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

// ErrNoTicket: the store holds no ticket of this hash: it was never made,
// was already spent, was replaced by a newer one, or was swept after it
// expired.
var ErrNoTicket = errors.New("no such OAuth ticket")

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
	// Ticketed: a ticket of the Mini App started the flow, in a browser
	// that need not be signed in (Connector.Ticket).
	Ticketed bool
}

// Ticket lets the person signed in in the Mini App start a flow in a
// browser where nobody is signed in to framy.
type Ticket struct {
	// Hash is the SHA-256 of the ticket the link carries (hashState).
	Hash     [sha256.Size]byte
	UserID   int64
	Provider string
	// ExpiresAt: TakeTicket returns an expired ticket as it finds it, as
	// Take does a flow.
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
	// PutTicket keeps the ticket in place of the person's earlier one for the
	// same provider, and forgets every ticket that had expired by now.
	PutTicket(ctx context.Context, t Ticket, now time.Time) error
	// TakeTicket removes the ticket and returns it, to exactly one of any
	// number of concurrent callers; the rest get ErrNoTicket.
	TakeTicket(ctx context.Context, hash [sha256.Size]byte) (Ticket, error)
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
		INSERT INTO oauth_state (state_hash, user_id, provider, verifier, expires_at, ticketed)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, provider) DO UPDATE SET
		  state_hash = EXCLUDED.state_hash, verifier = EXCLUDED.verifier, expires_at = EXCLUDED.expires_at,
		  ticketed = EXCLUDED.ticketed`,
		f.State[:], f.UserID, f.Provider, f.Verifier, f.ExpiresAt, f.Ticketed)
	if err != nil {
		return fmt.Errorf("keep the OAuth flow: %w", err)
	}
	return nil
}

func (s *PGStore) Take(ctx context.Context, state [sha256.Size]byte) (Flow, error) {
	f := Flow{State: state}
	err := s.pool.QueryRow(ctx, `
		DELETE FROM oauth_state WHERE state_hash = $1
		RETURNING user_id, provider, verifier, expires_at, ticketed`, state[:],
	).Scan(&f.UserID, &f.Provider, &f.Verifier, &f.ExpiresAt, &f.Ticketed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Flow{}, ErrNoFlow
	}
	if err != nil {
		return Flow{}, fmt.Errorf("take the OAuth flow: %w", err)
	}
	return f, nil
}

func (s *PGStore) PutTicket(ctx context.Context, t Ticket, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM oauth_ticket WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("sweep the OAuth tickets: %w", err)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_ticket (ticket_hash, user_id, provider, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, provider) DO UPDATE SET
		  ticket_hash = EXCLUDED.ticket_hash, expires_at = EXCLUDED.expires_at`,
		t.Hash[:], t.UserID, t.Provider, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("keep the OAuth ticket: %w", err)
	}
	return nil
}

func (s *PGStore) TakeTicket(ctx context.Context, hash [sha256.Size]byte) (Ticket, error) {
	t := Ticket{Hash: hash}
	err := s.pool.QueryRow(ctx, `
		DELETE FROM oauth_ticket WHERE ticket_hash = $1
		RETURNING user_id, provider, expires_at`, hash[:],
	).Scan(&t.UserID, &t.Provider, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrNoTicket
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("take the OAuth ticket: %w", err)
	}
	return t, nil
}
