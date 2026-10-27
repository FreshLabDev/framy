// SPDX-License-Identifier: Apache-2.0
//go:build integration

// Sessions against a real PostgreSQL, and sign-in end to end with core:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/auth/
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/migrations"
)

// These tests own the ids 8000 to 8999.
const (
	pgAnn = int64(8001)
	pgBob = int64(8002)
	pgNew = int64(8003)
)

func testStore(t *testing.T) *db.Store {
	t.Helper()
	raw := os.Getenv("FRAMY_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set FRAMY_TEST_DATABASE_URL to run the database tests")
	}
	if u, err := url.Parse(raw); err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatal("FRAMY_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\"")
	}
	ctx := context.Background()
	store, err := db.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "deploy", "core-init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	pool := store.Pool()
	if _, err := pool.Exec(ctx, string(bootstrap)); err != nil {
		t.Fatalf("core bootstrap: %v", err)
	}
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reset := func() {
		for _, stmt := range []string{
			`TRUNCATE framy.session`,
			`DELETE FROM core.presence WHERE telegram_user_id BETWEEN 8000 AND 8999`,
			`DELETE FROM core.person WHERE telegram_user_id BETWEEN 8000 AND 8999`,
		} {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	reset()
	t.Cleanup(reset)
	return store
}

func TestPGSessions(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	sessions := NewPGSessions(store.Pool())
	for _, id := range []int64{pgAnn, pgBob} {
		if err := store.Touch(ctx, tg.User{ID: id, FirstName: "P"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Truncate(time.Second)
	mk := func(secret string, user int64, kind Kind, expires time.Time) Session {
		return Session{Hash: sha256.Sum256([]byte(secret)), UserID: user, Kind: kind, Created: now, Expires: expires}
	}
	live := mk("live", pgAnn, KindSite, now.Add(time.Hour))
	if err := sessions.Create(ctx, live); err != nil {
		t.Fatal(err)
	}

	got, ok, err := sessions.Lookup(ctx, live.Hash, KindSite, now)
	if err != nil || !ok || got.UserID != pgAnn || got.Kind != KindSite || !got.Expires.Equal(live.Expires) {
		t.Fatalf("Lookup = %+v, %v, %v", got, ok, err)
	}
	if _, ok, _ := sessions.Lookup(ctx, live.Hash, KindMiniApp, now); ok {
		t.Error("a site session was found as a Mini App token")
	}
	if _, ok, _ := sessions.Lookup(ctx, live.Hash, KindSite, live.Expires); ok {
		t.Error("a session was found at its expiry")
	}
	if _, ok, _ := sessions.Lookup(ctx, sha256.Sum256([]byte("nobody")), KindSite, now); ok {
		t.Error("an unknown hash was found")
	}
	if err := sessions.Create(ctx, live); err == nil {
		t.Error("the same hash was stored twice")
	}
	if err := sessions.Create(ctx, mk("ghost", 8888, KindSite, now.Add(time.Hour))); err == nil {
		t.Error("a session for a person core has not seen was stored")
	}

	// Delete, DeleteUser, DeleteExpired.
	if err := sessions.Delete(ctx, live.Hash); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Delete(ctx, live.Hash); err != nil {
		t.Errorf("deleting a gone session: %v", err)
	}
	for i, s := range []Session{
		mk("a1", pgAnn, KindSite, now.Add(time.Hour)),
		mk("a2", pgAnn, KindMiniApp, now.Add(time.Hour)),
		mk("b1", pgBob, KindSite, now.Add(time.Hour)),
		mk("old", pgBob, KindSite, now.Add(-48*time.Hour)),
	} {
		if err := sessions.Create(ctx, s); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if n, err := sessions.DeleteExpired(ctx, now.Add(-24*time.Hour)); err != nil || n != 1 {
		t.Errorf("DeleteExpired = %d, %v", n, err)
	}
	if n, err := sessions.DeleteUser(ctx, pgAnn); err != nil || n != 2 {
		t.Errorf("DeleteUser = %d, %v", n, err)
	}
	if _, ok, _ := sessions.Lookup(ctx, sha256.Sum256([]byte("b1")), KindSite, now); !ok {
		t.Error("another person's session went with them")
	}

	// A person's deletion takes their sessions.
	if _, err := store.Pool().Exec(ctx, `DELETE FROM core.presence WHERE telegram_user_id = $1`, pgBob); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM core.person WHERE telegram_user_id = $1`, pgBob); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM framy.session`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d sessions left after the person went, %v", left, err)
	}
}

// The site and the bot are one account, and what the database holds of a
// session is a hash.
func TestPGSignInEndToEnd(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	h := newHarness(t, withSessions(NewPGSessions(store.Pool())), withPeople(store))

	// Ann has talked to the bot: core has her names.
	if err := store.Touch(ctx, tg.User{ID: pgAnn, FirstName: "Ann", LastName: "Lee", Username: "ann_lee", LanguageCode: "en"}, nil); err != nil {
		t.Fatal(err)
	}
	h.idp.userID, h.idp.name, h.idp.username = pgAnn, "Ann Lee", "ann_lee"
	b := h.browser()
	if cb := b.signIn("/"); cb.code != http.StatusSeeOther || cb.location() != "/" {
		t.Fatalf("callback: %d -> %q", cb.code, cb.location())
	}
	if id, ok := b.signedIn(); !ok || id != pgAnn {
		t.Fatalf("signed in as %d (%v)", id, ok)
	}

	// One person in core, names as the bot wrote them.
	var n int
	var first, last, uname *string
	if err := store.Pool().QueryRow(ctx,
		`SELECT count(*) OVER (), first_name, last_name, username FROM core.person WHERE telegram_user_id = $1`, pgAnn).
		Scan(&n, &first, &last, &uname); err != nil {
		t.Fatal(err)
	}
	if n != 1 || first == nil || *first != "Ann" || last == nil || *last != "Lee" || uname == nil || *uname != "ann_lee" {
		t.Errorf("core.person = %d rows, %v %v %v", n, deref(first), deref(last), deref(uname))
	}

	// The row holds the SHA-256 of the cookie and the cookie nowhere.
	secret := b.cookies[SessionCookie]
	sum := sha256.Sum256([]byte(secret))
	var stored, rowText string
	var user int64
	if err := store.Pool().QueryRow(ctx,
		`SELECT encode(token_hash, 'hex'), user_id, to_jsonb(s)::text FROM framy.session s`).Scan(&stored, &user, &rowText); err != nil {
		t.Fatal(err)
	}
	if stored != hex.EncodeToString(sum[:]) || user != pgAnn {
		t.Errorf("stored %s for %d", stored, user)
	}
	for _, form := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), hex.EncodeToString([]byte(secret))} {
		if strings.Contains(rowText, form) {
			t.Errorf("the row holds the session id: %s", rowText)
		}
	}
	cols := map[string]bool{}
	rows, err := store.Pool().Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'framy' AND table_name = 'session'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		cols[c] = true
	}
	rows.Close()
	for _, want := range []string{"token_hash", "user_id", "kind", "created_at", "expires_at"} {
		if !cols[want] {
			t.Errorf("column %s is missing from %v", want, cols)
		}
	}
	if len(cols) != 5 {
		t.Errorf("columns = %v: a new column may hold something it should not", cols)
	}

	// The Mini App is the same account.
	app := h.browser()
	h.mini(app, signInitData(testBotToken, miniAppFields(h.clock.Now(), pgAnn)), http.StatusOK)
	if id, ok := app.signedIn(); !ok || id != pgAnn {
		t.Fatalf("Mini App signed in as %d (%v)", id, ok)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM core.person WHERE telegram_user_id = $1`, pgAnn).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d person rows after the Mini App, %v", n, err)
	}

	// Logout deletes the row; the other credential is untouched.
	if r := b.do(http.MethodPost, "/auth/logout", ""); r.code != http.StatusNoContent {
		t.Fatalf("logout: %d", r.code)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM framy.session WHERE kind = 'site'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d site sessions after logout, %v", n, err)
	}
	if _, ok := app.signedIn(); !ok {
		t.Error("logging out of the site ended the Mini App's token")
	}
	// Signing out everywhere ends it.
	if r := app.do(http.MethodPost, "/auth/logout/all", ""); r.code != http.StatusNoContent {
		t.Fatalf("logout/all: %d", r.code)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM framy.session`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d sessions after logout/all, %v", n, err)
	}
}

// Someone no bot has seen can sign in: touch makes them, so the session's
// reference to core.person holds.
func TestPGNewPersonSignsInOnTheSite(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	h := newHarness(t, withSessions(NewPGSessions(store.Pool())), withPeople(store))
	h.idp.userID, h.idp.name, h.idp.username = pgNew, "Nina", "nina_n"
	b := h.browser()
	b.signIn("")
	if id, ok := b.signedIn(); !ok || id != pgNew {
		t.Fatalf("signed in as %d (%v)", id, ok)
	}
	var first, uname string
	if err := store.Pool().QueryRow(ctx, `SELECT first_name, username FROM core.person WHERE telegram_user_id = $1`, pgNew).Scan(&first, &uname); err != nil {
		t.Fatal(err)
	}
	if first != "Nina" || uname != "nina_n" {
		t.Errorf("core.person = %q %q", first, uname)
	}
	// And the bot meeting her afterwards is the same row.
	if err := store.Touch(ctx, tg.User{ID: pgNew, FirstName: "Nina", LastName: "N", Username: "nina_n"}, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM core.person WHERE telegram_user_id = $1`, pgNew).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d rows, %v", n, err)
	}
}

func TestPGMiniAppTokenExpiresAndIsSwept(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	h := newHarness(t, withSessions(NewPGSessions(store.Pool())), withPeople(store))
	app := h.browser()
	h.mini(app, signInitData(testBotToken, miniAppFields(h.clock.Now(), pgBob)), http.StatusOK)
	if _, ok := app.signedIn(); !ok {
		t.Fatal("not signed in")
	}
	h.clock.Advance(MiniAppTokenLife + time.Minute)
	if _, ok := app.signedIn(); ok {
		t.Error("an expired token works")
	}
	// The row stays a day, then the next sign-in sweeps it.
	var n int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM framy.session`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows, %v", n, err)
	}
	h.clock.Advance(sweepGrace + time.Hour)
	h.mini(h.browser(), signInitData(testBotToken, miniAppFields(h.clock.Now(), pgAnn)), http.StatusOK)
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM framy.session`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d rows after the sweep, want the new token only, %v", n, err)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}
