// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Migration struct {
	Version int
	Name    string
}

var migrationFilePattern = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// migrationLockKey serializes migrations across instances. Any constant works
// as long as it is stable across builds and differs from the other bots on the
// same server.
const migrationLockKey int64 = 0x6672616d79 // "framy"

// The limits a migration runs under. framy's schema is production's and
// the test bot's at once, and the test bot applies dev's migrations while
// production serves people: a migration that queues for a table's lock
// holds every query to that table behind it, and one that rewrites a large
// table holds its lock as long as it runs. lock_timeout gives the lock
// back, and the migration is tried again a little later; statement_timeout
// ends a migration that runs too long. A file may raise either with its own
// SET LOCAL.
var (
	migrationLockTimeout      = "5s"
	migrationStatementTimeout = "1min"
	// migrationTries is how many times a migration that waited too long
	// for a lock is tried, migrationWait apart, before the start fails.
	migrationTries = 6
	migrationWait  = 5 * time.Second
)

// lockNotAvailable is the SQLSTATE of a lock_timeout.
const lockNotAvailable = "55P03"

// Migrate applies every migration in fsys that schema_migrations has not seen,
// in version order, each in its own transaction under the limits above. Then
// each of after runs in a transaction of its own, still under the lock and
// the limits: River's migrations take no lock of their own.
//
// The ledger keeps the SHA-256 of each file it applied, and a file that
// differs from the one applied stops the start: an applied migration is
// never edited (AGENTS.md, Migrations). A row from before the ledger kept
// sums takes the sum of the file as it is now.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS, after ...func(context.Context, pgx.Tx) error) error {
	migrations, err := ListMigrations(fsys)
	if err != nil {
		return err
	}

	// A session-level lock on a dedicated connection: during a rolling restart
	// the second process waits for the first to finish, then finds every
	// migration applied.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer conn.Release()
	// Waiting for another process's migrations is not a statement that
	// hangs: the session's statement_timeout (Connect) is lifted for the
	// wait and put back before the connection goes back to the pool.
	if _, err := conn.Exec(ctx, `SET statement_timeout = 0`); err != nil {
		return fmt.Errorf("lift the statement timeout for the migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `RESET statement_timeout`) }()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		// Releasing the connection drops the lock anyway; the background
		// context lets the unlock run when ctx is already canceled.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		);
		ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS sha256 text
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	for _, m := range migrations {
		sql, err := fs.ReadFile(fsys, m.Name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.Name, err)
		}
		sum := sha256.Sum256(sql)
		digest := hex.EncodeToString(sum[:])
		var applied bool
		var kept *string
		err = conn.QueryRow(ctx, `SELECT true, sha256 FROM schema_migrations WHERE version=$1`, m.Version).Scan(&applied, &kept)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", m.Name, err)
		}
		switch {
		case applied && kept == nil:
			if _, err := conn.Exec(ctx, `UPDATE schema_migrations SET sha256=$2 WHERE version=$1 AND sha256 IS NULL`, m.Version, digest); err != nil {
				return fmt.Errorf("record the sum of migration %s: %w", m.Name, err)
			}
			continue
		case applied && *kept != digest:
			return fmt.Errorf("migration %s differs from the one applied (sha256 %.12s then, %.12s now): an applied migration is never edited, so put the change in a new one", m.Name, *kept, digest)
		case applied:
			continue
		}
		err = bounded(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,sha256) VALUES($1,$2,$3)`, m.Version, m.Name, digest)
			return err
		})
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
	}
	for _, step := range after {
		if err := bounded(ctx, conn, func(tx pgx.Tx) error { return step(ctx, tx) }); err != nil {
			return err
		}
	}
	return nil
}

// bounded runs fn in a transaction under the migrations' limits, again
// while it fails for want of a lock.
func bounded(ctx context.Context, conn *pgxpool.Conn, fn func(pgx.Tx) error) error {
	for try := 1; ; try++ {
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
				migrationLockTimeout, migrationStatementTimeout); err != nil {
				return err
			}
			return fn(tx)
		})
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != lockNotAvailable {
			return err
		}
		if try >= migrationTries {
			return fmt.Errorf("waited %d times for a lock that a longer transaction holds: %w", try, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(migrationWait):
		}
	}
}

// ListMigrations returns the migrations in fsys in version order. Two files
// with the same version are an error: only one of them would ever be applied.
func ListMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", entry.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", prev, entry.Name(), version)
		}
		seen[version] = entry.Name()
		out = append(out, Migration{Version: version, Name: entry.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
