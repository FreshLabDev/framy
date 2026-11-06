// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The SQL in this package needs a real PostgreSQL:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/db/
package db

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/migrations"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	rawURL := os.Getenv("FRAMY_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set FRAMY_TEST_DATABASE_URL to run the database tests")
	}
	// These tests truncate what they touch. The production database is shared
	// with the other bots, so refuse anything that is not visibly scratch.
	if name := databaseName(rawURL); !strings.Contains(name, "test") {
		t.Fatalf("FRAMY_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\", got %q", name)
	}
	ctx := context.Background()
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "deploy", "core-init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Connect(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(ctx, string(bootstrap)); err != nil {
		t.Fatalf("core bootstrap: %v", err)
	}
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reset := func() {
		for _, stmt := range []string{
			`UPDATE framy.runtime_state SET telegram_offset=0`,
			`TRUNCATE core.person, core.chat CASCADE`,
			`TRUNCATE core.language_observation`,
		} {
			if _, err := store.pool.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	reset()
	t.Cleanup(reset)
	return store
}

func databaseName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(parsed.Path, "/")
}

func TestMigrateIsRepeatable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	want, err := ListMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM framy.schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(want) {
		t.Fatalf("schema_migrations has %d rows, want %d", applied, len(want))
	}
}

func TestOffsetOnlyMovesForward(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, off := range []int64{10, 7} {
		if err := store.AdvanceOffset(ctx, off); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Offset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 10 {
		t.Fatalf("offset = %d, want 10", got)
	}
}

func TestTouchRecordsPersonAndGroup(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := tg.User{ID: 42, FirstName: "Ann", LanguageCode: "uk"}
	if err := store.Touch(ctx, user, &tg.Chat{ID: -1001, Type: "supergroup", Title: "Pictures"}); err != nil {
		t.Fatal(err)
	}
	// A DM is not a chat in core: the person is the subject.
	if err := store.Touch(ctx, user, &tg.Chat{ID: 42, Type: "private"}); err != nil {
		t.Fatal(err)
	}
	var people, chats int
	if err := store.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM core.person), (SELECT count(*) FROM core.chat)`).Scan(&people, &chats); err != nil {
		t.Fatal(err)
	}
	if people != 1 || chats != 1 {
		t.Fatalf("core has %d people and %d chats, want 1 and 1", people, chats)
	}
}

// A pick is a manual claim in core; Follow Telegram withdraws framy's claim
// and the client's language answers again.
func TestLanguageGoesThroughCore(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := tg.User{ID: 42, FirstName: "Ann", LanguageCode: "uk"}
	if err := store.Touch(ctx, user, &tg.Chat{ID: 42, Type: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserLang(ctx, 42, "pl"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.UserLang(ctx, 42); err != nil || got != "pl" {
		t.Fatalf("after a pick: %q, %v", got, err)
	}
	if err := store.ClearUserLang(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if got, err := store.UserLang(ctx, 42); err != nil || got != "uk" {
		t.Fatalf("after Follow Telegram: %q, %v", got, err)
	}

	if got, err := store.ChatLang(ctx, -1001); err != nil || got != "" {
		t.Fatalf("a group with no language: %q, %v", got, err)
	}
	if err := store.SetChatLang(ctx, -1001, "be"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ChatLang(ctx, -1001); err != nil || got != "be" {
		t.Fatalf("group language: %q, %v", got, err)
	}
	// The group's language is not the person's.
	if got, _ := store.UserLang(ctx, 42); got != "uk" {
		t.Fatalf("the group's pick changed the person's language to %q", got)
	}
	if err := store.ClearChatLang(ctx, -1001); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ChatLang(ctx, -1001); err != nil || got != "" {
		t.Fatalf("after Automatic: %q, %v", got, err)
	}
}

// A process's connections carry its name, so production and the test bot
// are told apart in pg_stat_activity, and a statement or a transaction left
// open has an end. DATABASE_URL may say otherwise.
func TestConnectNamesAndBoundsItsConnections(t *testing.T) {
	testStore(t)
	ctx := context.Background()
	raw := os.Getenv("FRAMY_TEST_DATABASE_URL")
	show := func(s *Store, name string) string {
		t.Helper()
		var v string
		if err := s.pool.QueryRow(ctx, `SELECT current_setting($1)`, name).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	test, err := Connect(ctx, raw, WithStack("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	for name, want := range map[string]string{
		"application_name": "framy-test", "statement_timeout": "30s", "idle_in_transaction_session_timeout": "10min", "search_path": "framy",
	} {
		if got := show(test, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if c := test.pool.Config(); c.MaxConns != 8 || c.MaxConnIdleTime != 5*time.Minute {
		t.Errorf("pool: %d connections, idle %s", c.MaxConns, c.MaxConnIdleTime)
	}

	prod, err := Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	if got := show(prod, "application_name"); got != "framy" {
		t.Errorf("production's connections are %q", got)
	}

	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	own, err := Connect(ctx, raw+sep+"statement_timeout=5000&pool_max_conns=3&application_name=framy-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	if got := show(own, "statement_timeout"); got != "5s" {
		t.Errorf("the URL's statement_timeout gave %q", got)
	}
	if got := show(own, "application_name"); got != "framy-debug" {
		t.Errorf("the URL's application_name gave %q", got)
	}
	if c := own.pool.Config(); c.MaxConns != 3 {
		t.Errorf("the URL's pool_max_conns gave %d", c.MaxConns)
	}
}

// The wait for another process's migrations outlasts the statement timeout,
// which is back on the connection afterwards.
func TestMigrateWaitsForTheLockPastTheStatementTimeout(t *testing.T) {
	testStore(t)
	ctx := context.Background()
	raw := os.Getenv("FRAMY_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	store, err := Connect(ctx, raw+sep+"statement_timeout=500&pool_max_conns=1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	holder, err := other.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_, _ = holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
		holder.Release()
	}()
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate behind another's lock: %v", err)
	}
	var v string
	if err := store.pool.QueryRow(ctx, `SELECT current_setting('statement_timeout')`).Scan(&v); err != nil || v != "500ms" {
		t.Errorf("after the migrations the statement timeout is %q: %v", v, err)
	}
}
