// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The flow against a real PostgreSQL and the real key store, with a stand-in
// for OpenRouter:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/oauth/
package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/migrations"
)

func testPool(t *testing.T) *pgxpool.Pool {
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
			`TRUNCATE framy.connection, framy.oauth_state`,
			`INSERT INTO core.person (telegram_user_id) VALUES (5001), (5002) ON CONFLICT DO NOTHING`,
		} {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	reset()
	t.Cleanup(reset)
	return pool
}

func testKEK(t *testing.T) *keys.KEK {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	kek, err := keys.ParseKEK([]byte("1:" + base64.StdEncoding.EncodeToString(raw) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return kek
}

// dbHarness is the harness with the database store and the real key store in
// place of the in-memory ones.
type dbHarness struct {
	*harness
	pool *pgxpool.Pool
	svc  *keys.Service
}

func newDBHarness(t *testing.T) *dbHarness {
	t.Helper()
	pool := testPool(t)
	h := newHarness(t)
	svc, err := keys.New(keys.Config{
		Pool: pool, KEK: testKEK(t), HTTP: h.or.Client(), UserAgent: "framy-test",
		OpenRouterBase: h.or.URL + "/api/v1",
		Log:            slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.newConnector(Config{Store: NewPGStore(pool), Keys: svc})
	return &dbHarness{harness: h, pool: pool, svc: svc}
}

func (d *dbHarness) count(table string) int {
	d.t.Helper()
	var n int
	if err := d.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		d.t.Fatal(err)
	}
	return n
}

func (d *dbHarness) verifierOf(state string) string {
	d.t.Helper()
	h := hashState(state)
	var v string
	if err := d.pool.QueryRow(context.Background(), `SELECT verifier FROM oauth_state WHERE state_hash = $1`, h[:]).Scan(&v); err != nil {
		d.t.Fatal(err)
	}
	return v
}

func TestPGStoreKeepsTheStoreContract(t *testing.T) {
	pool := testPool(t)
	testStoreContract(t, func(t *testing.T) Store {
		if _, err := pool.Exec(context.Background(), `TRUNCATE framy.oauth_state`); err != nil {
			t.Fatal(err)
		}
		return NewPGStore(pool)
	})
}

func TestTwoPeopleConnectAndEachKeyIsEncryptedForItsOwner(t *testing.T) {
	d := newDBHarness(t)
	ctx := context.Background()
	a, b := d.browser(ann), d.browser(bob)

	// Both flows are open at once.
	annAuth, bobAuth := a.start(), b.start()
	annState := mustQuery(t, annAuth).Get("state")
	annVerifier, bobVerifier := d.verifierOf(annState), d.verifierOf(mustQuery(t, bobAuth).Get("state"))
	if annVerifier == bobVerifier || d.count("oauth_state") != 2 {
		t.Fatalf("%d flows, the verifiers differ: %v", d.count("oauth_state"), annVerifier != bobVerifier)
	}
	annTarget, bobTarget := a.approve(annAuth), b.approve(bobAuth)

	if got := b.callback(bobTarget); got != Connected {
		t.Fatalf("bob: %q\n%s", got, d.logs.String())
	}
	bobKey := d.or.lastKey()
	if got := a.callback(annTarget); got != Connected {
		t.Fatalf("ann: %q\n%s", got, d.logs.String())
	}
	annKey := d.or.lastKey()
	if annKey == bobKey {
		t.Fatal("one key for two people")
	}

	if d.count("oauth_state") != 0 || d.count("connection") != 2 {
		t.Errorf("%d flows and %d connections left", d.count("oauth_state"), d.count("connection"))
	}
	for user, want := range map[int64]string{ann: annKey, bob: bobKey} {
		list, err := d.svc.List(ctx, user)
		if err != nil || len(list) != 1 || list[0].Provider != "openrouter" || list[0].Last4 != want[len(want)-4:] || list[0].Status != keys.Valid {
			t.Fatalf("connections of %d: %+v, %v", user, list, err)
		}
		// The key opens for the person it was made for, and for nobody else.
		got, err := d.svc.JobKey(ctx, jobs.Job{ID: 1, UserID: user, Provider: "openrouter"})
		if err != nil || got != want {
			t.Errorf("the key of %d is not the one OpenRouter made for them (%v)", user, err)
		}
	}

	// At rest: no part of either key in the row, in any form.
	for _, key := range []string{annKey, bobKey} {
		rows, err := d.pool.Query(ctx, `SELECT c::text, ciphertext, wrapped_dek FROM connection c`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var dump string
			var ciphertext, wrapped []byte
			if err := rows.Scan(&dump, &ciphertext, &wrapped); err != nil {
				t.Fatal(err)
			}
			for _, form := range []string{key, key[:20], base64.StdEncoding.EncodeToString([]byte(key)), hex.EncodeToString([]byte(key))[:32]} {
				if strings.Contains(dump, form) || bytes.Contains(ciphertext, []byte(form)) || bytes.Contains(wrapped, []byte(form)) {
					t.Errorf("a row holds %q", form)
				}
			}
		}
		rows.Close()
	}
	d.assertNoSecrets(annKey, bobKey, annVerifier, bobVerifier)
}

func TestAConnectReplacesThePastedKey(t *testing.T) {
	d := newDBHarness(t)
	ctx := context.Background()
	pasted := "sk-or-v1-pasted" + randHex(24)
	d.or.set(func(f *fakeOpenRouter) { f.minted[pasted] = true })
	if _, err := d.svc.Save(ctx, ann, "openrouter", pasted); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.browser(ann).connect(); got != Connected {
		t.Fatalf("result %q", got)
	}
	got, err := d.svc.JobKey(ctx, jobs.Job{ID: 1, UserID: ann, Provider: "openrouter"})
	if err != nil || got != d.or.lastKey() || got == pasted {
		t.Errorf("the key in use is not the one OpenRouter just made (%v)", err)
	}
	if d.count("connection") != 1 {
		t.Errorf("%d connections for one person and provider", d.count("connection"))
	}
}

// Whatever goes wrong, the real store ends with no connection, no flow left,
// and nothing secret said.
func TestRefusalsSaveNothingInTheDatabase(t *testing.T) {
	for name, tc := range map[string]struct {
		want  Result
		setup func(d *dbHarness, b *browser, target string) (*browser, string)
	}{
		"a wrong state": {Expired, func(d *dbHarness, b *browser, target string) (*browser, string) {
			return b, withState(t, target, "not-the-state")
		}},
		"no cookie": {Expired, func(d *dbHarness, b *browser, target string) (*browser, string) {
			return d.browser(ann), target
		}},
		"another person": {Expired, func(d *dbHarness, b *browser, target string) (*browser, string) {
			b.user = bob
			return b, target
		}},
		"nobody": {SignedOut, func(d *dbHarness, b *browser, target string) (*browser, string) {
			b.user = 0
			return b, target
		}},
		"ten minutes later": {Expired, func(d *dbHarness, b *browser, target string) (*browser, string) {
			d.clock.Advance(FlowLifetime)
			return b, target
		}},
		"the exchange refused": {Refused, func(d *dbHarness, b *browser, target string) (*browser, string) {
			d.or.set(func(f *fakeOpenRouter) { f.force = 400 })
			return b, target
		}},
		"the exchange failing": {Unavailable, func(d *dbHarness, b *browser, target string) (*browser, string) {
			d.or.set(func(f *fakeOpenRouter) { f.force = 503 })
			return b, target
		}},
		"the check of the key refused": {Refused, func(d *dbHarness, b *browser, target string) (*browser, string) {
			d.or.set(func(f *fakeOpenRouter) { f.rejectKey = true })
			return b, target
		}},
		"a cancelled authorization": {Cancelled, func(d *dbHarness, b *browser, target string) (*browser, string) {
			state, _ := callbackParams(t, target)
			return b, CallbackPath + "?error=access_denied&state=" + url.QueryEscape(state)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDBHarness(t)
			b := d.browser(ann)
			authURL := b.start()
			state := mustQuery(t, authURL).Get("state")
			verifier := d.verifierOf(state)
			target := b.approve(authURL)
			_, code := callbackParams(t, target)

			use, target := tc.setup(d, b, target)
			if got := use.callback(target); got != tc.want {
				t.Fatalf("result %q, want %q\n%s", got, tc.want, d.logs.String())
			}
			if d.count("connection") != 0 {
				t.Error("a connection was saved")
			}
			// The flow of the cookie is spent. (A browser with no cookie
			// spends nothing: the flow it never had is still the owner's.)
			if name != "no cookie" && d.count("oauth_state") != 0 {
				t.Error("the flow outlived the callback")
			}
			d.assertNoSecrets(code, verifier, d.or.lastKey())
		})
	}
}

func TestAReplayedCallbackFindsNothing(t *testing.T) {
	d := newDBHarness(t)
	b := d.browser(ann)
	target := b.approve(b.start())
	cookie := b.jar[cookieName]
	if got := b.callback(target); got != Connected {
		t.Fatalf("first: %q", got)
	}
	b.jar[cookieName] = cookie
	if got := b.callback(target); got != Expired {
		t.Fatalf("replay: %q", got)
	}
	if _, exchanges, _ := d.or.counts(); exchanges != 1 {
		t.Errorf("%d exchanges", exchanges)
	}
	if d.count("connection") != 1 {
		t.Errorf("%d connections", d.count("connection"))
	}
}

func TestStartingAgainLeavesOneFlowPerPerson(t *testing.T) {
	d := newDBHarness(t)
	a := d.browser(ann)
	a.start()
	a.start()
	d.browser(bob).start()
	if n := d.count("oauth_state"); n != 2 {
		t.Errorf("%d flows for two people", n)
	}
}

func TestDeletingAPersonTakesTheirFlow(t *testing.T) {
	d := newDBHarness(t)
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `INSERT INTO core.person (telegram_user_id) VALUES (5009) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.pool.Exec(ctx, `DELETE FROM core.person WHERE telegram_user_id = 5009`) })
	d.browser(5009).start()
	if d.count("oauth_state") != 1 {
		t.Fatal("no flow")
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM core.person WHERE telegram_user_id = 5009`); err != nil {
		t.Fatal(err)
	}
	if n := d.count("oauth_state"); n != 0 {
		t.Errorf("%d flows left", n)
	}
}

func TestTheTableRefusesWhatTheCodeNeverWrites(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for name, stmt := range map[string]string{
		"a state hash of the wrong size": `INSERT INTO oauth_state VALUES ('\x00', 5001, 'openrouter', 'v', now())`,
		"an unknown provider":            `INSERT INTO oauth_state VALUES (decode(repeat('00', 32), 'hex'), 5001, 'github', 'v', now())`,
		"a person nobody knows":          `INSERT INTO oauth_state VALUES (decode(repeat('00', 32), 'hex'), 4242, 'openrouter', 'v', now())`,
	} {
		if _, err := pool.Exec(ctx, stmt); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
