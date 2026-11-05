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

// SessionsChannel is the PostgreSQL channel PGSessions notifies on when it
// ends sessions, in the transaction that ends them. Every process of framy
// remembers the sessions it found for a while (Auth.ForgetAll): production,
// the test bot, and the old process during a deploy. The notice carries
// nothing, so whoever LISTENs on the shared database, which asks for no
// rights, learns only that someone signed out.
const SessionsChannel = "framy_sessions"

// PGSessions keeps sessions in framy.session. The pool's search_path is
// framy, as everywhere in the service.
//
// Production and the test bot share the table (docs/design.md, Stacks),
// and a session works only on the stack that made it: a cookie or a token
// of the test bot, which runs code not yet released, signs nobody in on
// production. Ending a person's sessions ends them on every stack: the
// person is one.
type PGSessions struct {
	pool  *pgxpool.Pool
	stack string
}

// NewPGSessions keeps the sessions of stack (FRAMY_STACK, "" for
// production) in the pool's database.
func NewPGSessions(pool *pgxpool.Pool, stack string) *PGSessions {
	return &PGSessions{pool: pool, stack: stack}
}

// Create implements Sessions.
func (p *PGSessions) Create(ctx context.Context, s Session) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO session (token_hash, user_id, kind, created_at, expires_at, stack) VALUES ($1, $2, $3, $4, $5, $6)`,
		s.Hash[:], s.UserID, string(s.Kind), s.Created, s.Expires, p.stack)
	return err
}

// Lookup implements Sessions with one query on the primary key. A session
// of another stack is not found.
func (p *PGSessions) Lookup(ctx context.Context, hash [32]byte, kind Kind, now time.Time) (Session, bool, error) {
	s := Session{Hash: hash, Kind: kind}
	err := p.pool.QueryRow(ctx,
		`SELECT user_id, created_at, expires_at FROM session WHERE token_hash = $1 AND kind = $2 AND expires_at > $3 AND stack = $4`,
		hash[:], string(kind), now, p.stack).Scan(&s.UserID, &s.Created, &s.Expires)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Session{}, false, nil
	case err != nil:
		return Session{}, false, err
	}
	return s, true, nil
}

// Delete implements Sessions, and tells every process of framy
// (SessionsChannel).
func (p *PGSessions) Delete(ctx context.Context, hash [32]byte) error {
	_, err := p.end(ctx, `DELETE FROM session WHERE token_hash = $1`, hash[:])
	return err
}

// DeleteUser implements Sessions, and tells every process of framy
// (SessionsChannel).
func (p *PGSessions) DeleteUser(ctx context.Context, userID int64) (int64, error) {
	return p.end(ctx, `DELETE FROM session WHERE user_id = $1`, userID)
}

// end runs a delete of sessions and, when it ended any, notifies
// SessionsChannel as it commits.
func (p *PGSessions) end(ctx context.Context, sql string, arg any) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, arg)
		if err != nil {
			return err
		}
		if n = tag.RowsAffected(); n > 0 {
			_, err = tx.Exec(ctx, `SELECT pg_notify($1, '')`, SessionsChannel)
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteExpired implements Sessions.
func (p *PGSessions) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM session WHERE expires_at < $1`, before)
	return tag.RowsAffected(), err
}
