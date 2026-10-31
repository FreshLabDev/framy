// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kind says what a session is for. A cookie is read only as a KindSite and a
// bearer token only as a KindMiniApp, so one cannot stand in for the other.
type Kind string

const (
	// KindSite is the cookie the site keeps after Telegram Login.
	KindSite Kind = "site"
	// KindMiniApp is the bearer token a Mini App exchanges its initData for.
	KindMiniApp Kind = "miniapp"
)

// Session is a row of framy.session. The secret the browser holds is not in
// it, only the SHA-256 of that secret.
type Session struct {
	Hash    [32]byte
	UserID  int64
	Kind    Kind
	Created time.Time
	Expires time.Time
}

// Sessions is where sessions are kept. The Postgres one is the real thing;
// tests may bring their own.
type Sessions interface {
	// Create stores s. The person has to exist in core already.
	Create(ctx context.Context, s Session) error
	// Lookup finds the live session with this hash and kind: one that has not
	// expired at now. Not finding one is (zero, false, nil).
	Lookup(ctx context.Context, hash [32]byte, kind Kind, now time.Time) (Session, bool, error)
	// Delete ends one session. Ending one that is gone is not an error.
	Delete(ctx context.Context, hash [32]byte) error
	// DeleteUser ends every session of a person and says how many there were.
	DeleteUser(ctx context.Context, userID int64) (int64, error)
	// DeleteExpired removes what expired before the given time.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// PGSessions keeps sessions in framy.session. The pool's search_path is
// framy, as everywhere in the service.
type PGSessions struct{ pool *pgxpool.Pool }

// NewPGSessions keeps sessions in the pool's database.
func NewPGSessions(pool *pgxpool.Pool) *PGSessions { return &PGSessions{pool: pool} }

// Create implements Sessions.
func (p *PGSessions) Create(ctx context.Context, s Session) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO session (token_hash, user_id, kind, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		s.Hash[:], s.UserID, string(s.Kind), s.Created, s.Expires)
	return err
}

// Lookup implements Sessions with one query on the primary key.
func (p *PGSessions) Lookup(ctx context.Context, hash [32]byte, kind Kind, now time.Time) (Session, bool, error) {
	s := Session{Hash: hash, Kind: kind}
	err := p.pool.QueryRow(ctx,
		`SELECT user_id, created_at, expires_at FROM session WHERE token_hash = $1 AND kind = $2 AND expires_at > $3`,
		hash[:], string(kind), now).Scan(&s.UserID, &s.Created, &s.Expires)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Session{}, false, nil
	case err != nil:
		return Session{}, false, err
	}
	return s, true, nil
}

// Delete implements Sessions.
func (p *PGSessions) Delete(ctx context.Context, hash [32]byte) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM session WHERE token_hash = $1`, hash[:])
	return err
}

// DeleteUser implements Sessions.
func (p *PGSessions) DeleteUser(ctx context.Context, userID int64) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM session WHERE user_id = $1`, userID)
	return tag.RowsAffected(), err
}

// DeleteExpired implements Sessions.
func (p *PGSessions) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM session WHERE expires_at < $1`, before)
	return tag.RowsAffected(), err
}
