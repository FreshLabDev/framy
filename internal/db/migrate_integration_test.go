// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// probe is a migration of the tests' own, far past framy's, removed again
// when the test ends.
func probe(t *testing.T, store *Store, version, table string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = store.pool.Exec(ctx, `DROP TABLE IF EXISTS framy.`+table)
		_, _ = store.pool.Exec(ctx, `DELETE FROM framy.schema_migrations WHERE version = `+version)
	})
}

// A migration runs under a lock timeout and a statement timeout of its own,
// and the ledger keeps the sum of the file it applied.
func TestAMigrationRunsUnderItsOwnLimits(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	probe(t, store, "9001", "migration_probe")
	fsys := fstest.MapFS{"9001_probe.sql": {Data: []byte(`
		CREATE TABLE framy.migration_probe AS
		SELECT current_setting('lock_timeout') AS lock, current_setting('statement_timeout') AS statement`)}}
	if err := store.Migrate(ctx, fsys); err != nil {
		t.Fatal(err)
	}
	var lock, statement string
	if err := store.pool.QueryRow(ctx, `SELECT lock, statement FROM framy.migration_probe`).Scan(&lock, &statement); err != nil {
		t.Fatal(err)
	}
	if lock != "5s" || statement != "1min" {
		t.Errorf("the migration ran with lock_timeout %q and statement_timeout %q", lock, statement)
	}
	var sum *string
	if err := store.pool.QueryRow(ctx, `SELECT sha256 FROM framy.schema_migrations WHERE version = 9001`).Scan(&sum); err != nil || sum == nil || len(*sum) != 64 {
		t.Errorf("the ledger's sum: %v, %v", sum, err)
	}
}

// A file changed after it was applied stops the start, and a row from before
// the ledger kept sums takes the file's.
func TestAnEditedMigrationStopsTheStart(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	probe(t, store, "9002", "migration_edited")
	first := fstest.MapFS{"9002_edited.sql": {Data: []byte(`CREATE TABLE framy.migration_edited (id int)`)}}
	if err := store.Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	edited := fstest.MapFS{"9002_edited.sql": {Data: []byte(`CREATE TABLE framy.migration_edited (id bigint)`)}}
	err := store.Migrate(ctx, edited)
	if err == nil || !strings.Contains(err.Error(), "9002_edited.sql differs from the one applied") {
		t.Fatalf("an edited migration: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE framy.schema_migrations SET sha256 = NULL WHERE version = 9002`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, first); err != nil {
		t.Fatalf("a row without a sum: %v", err)
	}
	if err := store.Migrate(ctx, edited); err == nil {
		t.Fatal("the sum recorded for the old row did not hold")
	}
}

// A migration that waits for a lock a long transaction holds gives the lock
// back and tries again, and once the transaction ends it goes through.
func TestAMigrationBehindALongTransactionTriesAgain(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	probe(t, store, "9003", "migration_locked")
	if _, err := store.pool.Exec(ctx, `CREATE TABLE framy.migration_locked (id int)`); err != nil {
		t.Fatal(err)
	}
	oldLock, oldTries, oldWait := migrationLockTimeout, migrationTries, migrationWait
	migrationLockTimeout, migrationTries, migrationWait = "100ms", 3, 10*time.Millisecond
	t.Cleanup(func() { migrationLockTimeout, migrationTries, migrationWait = oldLock, oldTries, oldWait })

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE framy.migration_locked IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"9003_locked.sql": {Data: []byte(`ALTER TABLE framy.migration_locked ADD COLUMN note text`)}}
	err = store.Migrate(ctx, fsys)
	if err == nil || !strings.Contains(err.Error(), "waited 3 times for a lock") {
		t.Fatalf("behind a held lock: %v", err)
	}
	var applied bool
	_ = store.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM framy.schema_migrations WHERE version = 9003)`).Scan(&applied)
	if applied {
		t.Fatal("a migration that never got its lock is in the ledger")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, fsys); err != nil {
		t.Fatalf("once the lock is free: %v", err)
	}
}
