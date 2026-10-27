// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The service against a real PostgreSQL, with stand-ins for the providers:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/keys/
package keys

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/migrations"
)

const (
	ann = int64(5001)
	bob = int64(5002)
	eve = int64(5003)
)

// The people's keys. They are made, not written out, and each has a stretch
// no other test text contains, so a leak is easy to find.
var (
	annOpenAI  = "sk-proj-AnnZq81nT3vLw5RkE7yUo2Pd9Hs4Bj6Xc0MaGfIt"
	annGemini  = "AIzaSyAnn-Qw1Er2Ty3Ui4Op5As6Df7Gh8Jk9Lz"
	annRouter  = "sk-or-v1-" + strings.Repeat("a1b2c3d4", 8)
	bobOpenAI  = "sk-proj-BobMx47kV1cNq9ZsH3tYw6Re2Pu8Ld5Jf0AgBb"
	evesOpenAI = "sk-proj-EveHj62sP4vTn8KcX1qWy5Rz9Ua3Md7Lf0GbNe"
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
			`TRUNCATE framy.connection`,
			`INSERT INTO core.person (telegram_user_id) VALUES (5001), (5002), (5003) ON CONFLICT DO NOTHING`,
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

// providers stands in for the three providers' checks. A key it knows is
// accepted; the answer for any other, or for all, can be forced.
type providers struct {
	*httptest.Server
	mu      sync.Mutex
	known   map[string]bool
	force   int
	calls   []string
	keySeen []string
}

func newProviders(t *testing.T, known ...string) *providers {
	t.Helper()
	p := &providers{known: map[string]bool{}}
	for _, k := range known {
		p.known[k] = true
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if k := r.Header.Get("x-goog-api-key"); k != "" {
			key = k
		}
		p.mu.Lock()
		p.calls = append(p.calls, r.URL.Path)
		p.keySeen = append(p.keySeen, key)
		force, ok := p.force, p.known[key]
		p.mu.Unlock()
		status := http.StatusUnauthorized
		if ok {
			status = http.StatusOK
		}
		if force != 0 {
			status = force
		}
		w.WriteHeader(status)
		// Real providers echo the key in a refusal.
		_, _ = fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s"}}`, key)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *providers) accept(keys ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range keys {
		p.known[k] = true
	}
}

func (p *providers) forceStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.force = status
}

func (p *providers) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// newService builds a service on the pool with the given KEK, a log kept in
// the returned buffer and checks pointed at p.
func newService(t *testing.T, pool *pgxpool.Pool, kek *KEK, p *providers) (*Service, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	svc, err := New(Config{
		Pool: pool, KEK: kek, HTTP: p.Client(), UserAgent: "framy-test",
		OpenAIBase: p.URL, GeminiBase: p.URL, OpenRouterBase: p.URL + "/api/v1",
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, logs
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func job(user int64, provider string) jobs.Job {
	return jobs.Job{ID: 1, UserID: user, Provider: provider}
}

func asJobsError(t *testing.T, err error, class jobs.Class) *jobs.Error {
	t.Helper()
	var pe *jobs.Error
	if !errors.As(err, &pe) || pe.Class != class {
		t.Fatalf("err = %v, want a jobs.Error of class %s", err, class)
	}
	return pe
}

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM connection`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveKeepsAnEncryptedKeyAndShowsOnlyTheLastFour(t *testing.T) {
	pool := testPool(t)
	kek := testKEK(t, 1)
	p := newProviders(t, annOpenAI)
	svc, logs := newService(t, pool, kek, p)
	ctx := context.Background()

	c, err := svc.Save(ctx, ann, catalog.OpenAI, "  "+annOpenAI+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != Valid || c.Last4 != annOpenAI[len(annOpenAI)-4:] || c.Provider != "openai" || c.UserID != ann || len(c.ID) != 36 {
		t.Fatalf("saved %+v", c)
	}
	if p.callCount() != 1 || p.keySeen[0] != annOpenAI {
		t.Fatalf("the check saw %v, want the trimmed key once", p.keySeen)
	}

	// At rest: no part of the key, in any form, anywhere in the row.
	var dump string
	if err := pool.QueryRow(ctx, `SELECT c::text FROM connection c`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	var ciphertext, wrapped []byte
	var version int
	if err := pool.QueryRow(ctx, `SELECT ciphertext, wrapped_dek, kek_version FROM connection`).Scan(&ciphertext, &wrapped, &version); err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{annOpenAI, annOpenAI[:16], annOpenAI[12:30], base64.StdEncoding.EncodeToString([]byte(annOpenAI)), hex.EncodeToString([]byte(annOpenAI))[:32]} {
		if strings.Contains(dump, form) || bytes.Contains(ciphertext, []byte(form)) || bytes.Contains(wrapped, []byte(form)) {
			t.Errorf("the row holds %q", form)
		}
	}
	if version != 1 {
		t.Errorf("kek_version = %d", version)
	}

	// What a page gets holds no key either.
	list, err := svc.List(ctx, ann)
	if err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	asJSON, _ := json.Marshal(list)
	for _, text := range []string{string(asJSON), fmt.Sprintf("%+v %v %#v", list, list[0], list[0])} {
		if strings.Contains(text, annOpenAI[:16]) || strings.Contains(text, annOpenAI[:len(annOpenAI)-4]) {
			t.Errorf("a listing holds the key: %s", text)
		}
	}
	if !strings.Contains(string(asJSON), `"last4":"`+c.Last4+`"`) || strings.Contains(string(asJSON), "user_id") {
		t.Errorf("listing JSON = %s", asJSON)
	}

	got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI))
	if err != nil || got != annOpenAI {
		t.Fatalf("JobKey = %q, %v", got, err)
	}
	assertNoSecret(t, logs.String(), annOpenAI, kek)
}

func assertNoSecret(t *testing.T, text, key string, kek *KEK) {
	t.Helper()
	forms := []string{key, key[:14], key[len(key)-14:]}
	for _, k := range kek.keys {
		forms = append(forms, base64.StdEncoding.EncodeToString(k), base64.StdEncoding.EncodeToString(k)[:16])
	}
	for _, f := range forms {
		if strings.Contains(text, f) {
			t.Errorf("a secret reached the output: %q in %.300s", f, text)
		}
	}
}

func TestSaveReplacesTheKeyWithANewConnection(t *testing.T) {
	pool := testPool(t)
	kek := testKEK(t, 1)
	p := newProviders(t, annOpenAI, evesOpenAI)
	svc, _ := newService(t, pool, kek, p)
	ctx := context.Background()

	first, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.markSuccess(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Save(ctx, ann, catalog.OpenAI, evesOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Last4 != evesOpenAI[len(evesOpenAI)-4:] {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	// The new connection starts clean: nothing the old key did carries over.
	if second.Status != Valid || second.LastSuccessAt != nil || second.LastErrorClass != "" {
		t.Errorf("second = %+v", second)
	}
	if countRows(t, pool) != 1 {
		t.Fatal("a replaced key left a second row")
	}
	if got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI)); err != nil || got != evesOpenAI {
		t.Fatalf("JobKey = %q, %v", got, err)
	}
}

func TestSaveStoresNothingThatFailed(t *testing.T) {
	pool := testPool(t)
	p := newProviders(t, annOpenAI)
	svc, logs := newService(t, pool, testKEK(t, 1), p)
	ctx := context.Background()
	if _, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI); err != nil {
		t.Fatal(err)
	}
	calls := p.callCount()

	for name, tc := range map[string]struct {
		provider, key string
		force         int
		want          error
	}{
		"a key the provider refuses":   {catalog.OpenAI, bobOpenAI, 0, ErrRejected},
		"a provider that forbids":      {catalog.OpenAI, annOpenAI, 403, ErrForbidden},
		"a provider that is down":      {catalog.OpenAI, annOpenAI, 503, ErrUnavailable},
		"a provider that limits":       {catalog.OpenAI, annOpenAI, 429, ErrUnavailable},
		"a Gemini key the check fails": {catalog.Gemini, annGemini, 0, ErrRejected},
	} {
		t.Run(name, func(t *testing.T) {
			p.forceStatus(tc.force)
			defer p.forceStatus(0)
			_, err := svc.Save(ctx, bob, tc.provider, tc.key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.key[:14]) {
				t.Errorf("the error carries the key: %v", err)
			}
		})
	}
	if countRows(t, pool) != 1 {
		t.Errorf("a failed save stored a row")
	}

	// A failed replacement leaves the old key in place.
	if _, err := svc.Save(ctx, ann, catalog.OpenAI, bobOpenAI); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v", err)
	}
	if got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI)); err != nil || got != annOpenAI {
		t.Fatalf("the old key is gone: %q, %v", got, err)
	}

	// What is not a key never reaches the provider.
	calls = p.callCount()
	for name, key := range map[string]string{
		"empty":         "",
		"blank":         "   \n",
		"too short":     "sk-short",
		"a space in it": "sk-proj-abc def ghi jkl mno pqr",
		"a newline":     "sk-proj-abc\ndef-ghi-jkl-mno-pqr",
		"a control":     "sk-proj-abcdefghijklmn\x00opqrstu",
		"not ASCII":     "sk-proj-абвгдежзиклмнопрстуф",
		"too long":      "sk-" + strings.Repeat("a", 600),
	} {
		if _, err := svc.Save(ctx, bob, catalog.OpenAI, key); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := svc.Save(ctx, bob, "midjourney", annOpenAI); !errors.Is(err, ErrUnsupportedProvider) {
		t.Errorf("an unknown provider: %v", err)
	}
	if p.callCount() != calls {
		t.Error("something that is not a key was sent to a provider")
	}
	assertNoSecret(t, logs.String(), annOpenAI, svc.kek)
}

// A person with no KEK file configured: nothing is kept, and no run falls
// back to another key.
func TestAServiceWithoutAKEKRefusesEverything(t *testing.T) {
	var svc *Service
	ctx := context.Background()
	if _, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI); !errors.Is(err, ErrDisabled) {
		t.Errorf("Save: %v", err)
	}
	if _, err := svc.List(ctx, ann); !errors.Is(err, ErrDisabled) {
		t.Errorf("List: %v", err)
	}
	if err := svc.Delete(ctx, ann, catalog.OpenAI); !errors.Is(err, ErrDisabled) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := svc.Recheck(ctx, ann, catalog.OpenAI); !errors.Is(err, ErrDisabled) {
		t.Errorf("Recheck: %v", err)
	}
	if _, err := svc.Rotate(ctx); !errors.Is(err, ErrDisabled) {
		t.Errorf("Rotate: %v", err)
	}
	key, err := svc.JobKey(ctx, job(ann, catalog.OpenAI))
	asJobsError(t, err, jobs.Key)
	if key != "" {
		t.Error("a key came out of nothing")
	}
	if _, err := New(Config{}); err == nil {
		t.Error("New accepted no pool and no KEK")
	}
}

func TestOneConnectionPerPersonAndProvider(t *testing.T) {
	pool := testPool(t)
	p := newProviders(t, annOpenAI, annGemini, annRouter, bobOpenAI)
	svc, _ := newService(t, pool, testKEK(t, 1), p)
	ctx := context.Background()
	for provider, key := range map[string]string{catalog.OpenAI: annOpenAI, catalog.Gemini: annGemini, catalog.OpenRouter: annRouter} {
		if _, err := svc.Save(ctx, ann, provider, key); err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
	}
	if _, err := svc.Save(ctx, bob, catalog.OpenAI, bobOpenAI); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, ann)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].Provider != "gemini" || list[1].Provider != "openai" || list[2].Provider != "openrouter" {
		t.Errorf("order = %s, %s, %s", list[0].Provider, list[1].Provider, list[2].Provider)
	}
	if mine, _ := svc.List(ctx, bob); len(mine) != 1 || mine[0].UserID != bob {
		t.Errorf("bob's list = %+v", mine)
	}
	if none, err := svc.List(ctx, eve); err != nil || len(none) != 0 {
		t.Errorf("eve's list = %+v, %v", none, err)
	}
	// The same key in two people's hands is two connections.
	p.accept(annOpenAI)
	if _, err := svc.Save(ctx, bob, catalog.Gemini, annGemini); err != nil {
		t.Fatal(err)
	}
	if countRows(t, pool) != 5 {
		t.Errorf("rows = %d", countRows(t, pool))
	}
}

func TestSaveNeedsThePersonInCore(t *testing.T) {
	pool := testPool(t)
	p := newProviders(t, annOpenAI)
	svc, _ := newService(t, pool, testKEK(t, 1), p)
	if _, err := svc.Save(context.Background(), 987654321, catalog.OpenAI, annOpenAI); err == nil {
		t.Fatal("saved a key for a person core does not know")
	} else if strings.Contains(err.Error(), annOpenAI[:14]) {
		t.Errorf("the error carries the key: %v", err)
	}
}

func TestDeleteRemovesTheRow(t *testing.T) {
	pool := testPool(t)
	p := newProviders(t, annOpenAI, annGemini)
	svc, _ := newService(t, pool, testKEK(t, 1), p)
	ctx := context.Background()
	for provider, key := range map[string]string{catalog.OpenAI: annOpenAI, catalog.Gemini: annGemini} {
		if _, err := svc.Save(ctx, ann, provider, key); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Delete(ctx, ann, catalog.OpenAI); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, ann, catalog.OpenAI); !errors.Is(err, ErrNotFound) {
		t.Errorf("a second delete: %v", err)
	}
	if err := svc.Delete(ctx, bob, catalog.Gemini); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting another person's: %v", err)
	}
	if countRows(t, pool) != 1 {
		t.Fatalf("rows = %d, want ann's Gemini key alone", countRows(t, pool))
	}
	_, err := svc.JobKey(ctx, job(ann, catalog.OpenAI))
	asJobsError(t, err, jobs.Key)
	if got, err := svc.JobKey(ctx, job(ann, catalog.Gemini)); err != nil || got != annGemini {
		t.Errorf("the other key: %q, %v", got, err)
	}
}

func TestJobKeyIsThePayersAndNobodyElses(t *testing.T) {
	pool := testPool(t)
	p := newProviders(t, annOpenAI, bobOpenAI, annGemini)
	svc, logs := newService(t, pool, testKEK(t, 1), p)
	ctx := context.Background()
	for _, s := range []struct {
		user     int64
		provider string
		key      string
	}{{ann, catalog.OpenAI, annOpenAI}, {bob, catalog.OpenAI, bobOpenAI}, {ann, catalog.Gemini, annGemini}} {
		if _, err := svc.Save(ctx, s.user, s.provider, s.key); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		j    jobs.Job
		want string
	}{
		"ann at OpenAI": {job(ann, catalog.OpenAI), annOpenAI},
		"bob at OpenAI": {job(bob, catalog.OpenAI), bobOpenAI},
		"ann at Gemini": {job(ann, catalog.Gemini), annGemini},
	} {
		if got, err := svc.JobKey(ctx, tc.j); err != nil || got != tc.want {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}

	for name, j := range map[string]jobs.Job{
		"bob has no Gemini key":        job(bob, catalog.Gemini),
		"eve has none at all":          job(eve, catalog.OpenAI),
		"ann has no OpenRouter key":    job(ann, catalog.OpenRouter),
		"nobody at a provider unknown": job(ann, "midjourney"),
	} {
		got, err := svc.JobKey(ctx, j)
		pe := asJobsError(t, err, jobs.Key)
		if got != "" {
			t.Errorf("%s: a key came out: %q", name, got)
		}
		for _, k := range []string{annOpenAI, bobOpenAI, annGemini} {
			if strings.Contains(pe.Error(), k[:14]) {
				t.Errorf("%s: the error carries a key: %v", name, pe)
			}
		}
	}

	// A free run is framy's: a person's key must not pay for it.
	free := job(ann, catalog.OpenAI)
	free.Free = true
	got, err := svc.JobKey(ctx, free)
	asJobsError(t, err, jobs.Key)
	if got != "" {
		t.Error("a free run got a person's key")
	}
	assertNoSecret(t, logs.String(), annOpenAI, svc.kek)
}

// A key the provider refused is not handed out again; one that merely
// lacks billing, verification or credit still is, since the run costs
// nothing when refused and is how the status heals.
func TestJobKeyByStatus(t *testing.T) {
	pool := testPool(t)
	p := newProviders(t, annOpenAI)
	svc, _ := newService(t, pool, testKEK(t, 1), p)
	ctx := context.Background()
	if _, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI); err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{Valid, Verified, NeedsBilling, NeedsVerification, NoCredits} {
		if _, err := pool.Exec(ctx, `UPDATE connection SET status = $1`, string(status)); err != nil {
			t.Fatal(err)
		}
		if got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI)); err != nil || got != annOpenAI {
			t.Errorf("%s: %q, %v", status, got, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE connection SET status = 'invalid'`); err != nil {
		t.Fatal(err)
	}
	got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI))
	asJobsError(t, err, jobs.Key)
	if got != "" {
		t.Error("an invalid key was handed out")
	}
}

// What binds a key to its record is checked on the way out: a row altered in
// the database, or moved, does not decrypt, and says so without the key.
func TestAlteredRowsDoNotDecrypt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// The KEK has two versions; rows are made under 2, the current.
	kek := testKEK(t, 1, 2)
	p := newProviders(t, annOpenAI, bobOpenAI, annGemini)
	svc, logs := newService(t, pool, kek, p)

	type who struct {
		user     int64
		provider string
	}
	baseline := map[who]string{
		{ann, catalog.OpenAI}: annOpenAI,
		{bob, catalog.OpenAI}: bobOpenAI,
		{ann, catalog.Gemini}: annGemini,
	}
	reset := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `TRUNCATE connection`); err != nil {
			t.Fatal(err)
		}
		for w, key := range baseline {
			if _, err := svc.Save(ctx, w.user, w.provider, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	// read is JobKey over every pair: whatever comes out is the key of that
	// very record, never another's.
	read := func(t *testing.T) map[who]bool {
		t.Helper()
		ok := map[who]bool{}
		for _, user := range []int64{ann, bob, eve} {
			for _, provider := range Providers {
				w := who{user, provider}
				got, err := svc.JobKey(ctx, job(user, provider))
				if err != nil {
					asJobsError(t, err, jobs.Key)
					if got != "" {
						t.Errorf("%v: a key came out with an error", w)
					}
					continue
				}
				if got != baseline[w] {
					t.Errorf("%v: the key of another record came out: %.12s", w, got)
				}
				ok[w] = true
			}
		}
		return ok
	}
	reset()
	if ok := read(t); len(ok) != 3 {
		t.Fatalf("the control does not read: %v", ok)
	}

	for _, tc := range []struct {
		name   string
		stmt   string
		args   []any
		victim who
	}{
		{"a row moved to another person", `UPDATE connection SET user_id = $1 WHERE user_id = $2 AND provider = 'gemini'`, []any{eve, ann}, who{eve, catalog.Gemini}},
		{"a row moved to another provider", `UPDATE connection SET provider = 'openrouter' WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.OpenRouter}},
		{"a row given another id", `UPDATE connection SET id = gen_random_uuid() WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.Gemini}},
		{"two people's ciphertexts swapped", `UPDATE connection c SET ciphertext = o.ciphertext FROM connection o WHERE c.user_id = $1 AND c.provider = 'openai' AND o.user_id = $2 AND o.provider = 'openai'`, []any{ann, bob}, who{ann, catalog.OpenAI}},
		{"a key under another person's wrap", `UPDATE connection c SET wrapped_dek = o.wrapped_dek FROM connection o WHERE c.user_id = $1 AND c.provider = 'openai' AND o.user_id = $2 AND o.provider = 'openai'`, []any{ann, bob}, who{ann, catalog.OpenAI}},
		{"the KEK version changed to another that exists", `UPDATE connection SET kek_version = 1 WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.Gemini}},
		{"a KEK version the file does not have", `UPDATE connection SET kek_version = 9 WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.Gemini}},
		{"a cut ciphertext", `UPDATE connection SET ciphertext = substring(ciphertext from 1 for 20) WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.Gemini}},
		{"a flipped byte of the wrap", `UPDATE connection SET wrapped_dek = set_byte(wrapped_dek, 30, get_byte(wrapped_dek, 30) # 1) WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.Gemini}},
		{"a flipped byte of the ciphertext", `UPDATE connection SET ciphertext = set_byte(ciphertext, 30, get_byte(ciphertext, 30) # 1) WHERE user_id = $1 AND provider = 'gemini'`, []any{ann}, who{ann, catalog.Gemini}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			if _, err := pool.Exec(ctx, tc.stmt, tc.args...); err != nil {
				t.Fatal(err)
			}
			if ok := read(t); ok[tc.victim] {
				t.Errorf("%v still reads after the change", tc.victim)
			}
		})
	}

	if !strings.Contains(logs.String(), "a stored key cannot be decrypted") {
		t.Error("a failure to decrypt was not logged")
	}
	for _, key := range baseline {
		assertNoSecret(t, logs.String(), key, kek)
	}
}

func TestRecheck(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	kek := testKEK(t, 1)
	p := newProviders(t, annOpenAI)
	svc, logs := newService(t, pool, kek, p)
	saved, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	set := func(status Status, class string) {
		t.Helper()
		_, err := pool.Exec(ctx, `UPDATE connection SET status = $1, last_error_class = NULLIF($2, '')`, string(status), class)
		if err != nil {
			t.Fatal(err)
		}
	}

	// The provider gets the stored key, decrypted.
	c, err := svc.Recheck(ctx, ann, catalog.OpenAI)
	if err != nil || c.Status != Valid || c.ID != saved.ID {
		t.Fatalf("recheck = %+v, %v", c, err)
	}
	if last := p.keySeen[len(p.keySeen)-1]; last != annOpenAI {
		t.Errorf("the provider was sent %q", last)
	}

	// A generation already worked: the list of models adds nothing to that.
	set(Verified, "")
	if c, err = svc.Recheck(ctx, ann, catalog.OpenAI); err != nil || c.Status != Verified {
		t.Errorf("verified: %+v, %v", c, err)
	}
	// The person fixed the account and presses re-check: the problem the
	// status named is no longer known to be there.
	for _, status := range []Status{NeedsBilling, NeedsVerification, NoCredits} {
		set(status, "money")
		c, err = svc.Recheck(ctx, ann, catalog.OpenAI)
		if err != nil || c.Status != Valid || c.LastErrorClass != "" {
			t.Errorf("%s: %+v, %v", status, c, err)
		}
	}

	// The provider refuses the key now.
	p.forceStatus(401)
	if c, err = svc.Recheck(ctx, ann, catalog.OpenAI); err != nil || c.Status != Invalid {
		t.Fatalf("a refused key: %+v, %v", c, err)
	}
	if _, err := svc.JobKey(ctx, job(ann, catalog.OpenAI)); err == nil {
		t.Error("an invalid key was handed out")
	}
	// And accepts it again.
	p.forceStatus(0)
	if c, err = svc.Recheck(ctx, ann, catalog.OpenAI); err != nil || c.Status != Valid {
		t.Fatalf("accepted again: %+v, %v", c, err)
	}

	// An answer about nothing changes nothing.
	set(NeedsBilling, "money")
	for status, want := range map[int]error{503: ErrUnavailable, 429: ErrUnavailable, 403: ErrForbidden} {
		p.forceStatus(status)
		c, err := svc.Recheck(ctx, ann, catalog.OpenAI)
		if !errors.Is(err, want) || c.ID != "" {
			t.Errorf("%d: %+v, %v", status, c, err)
		}
		var now Status
		if err := pool.QueryRow(ctx, `SELECT status FROM connection`).Scan(&now); err != nil || now != NeedsBilling {
			t.Errorf("%d changed the status to %q (%v)", status, now, err)
		}
	}
	p.forceStatus(0)

	if _, err := svc.Recheck(ctx, bob, catalog.OpenAI); !errors.Is(err, ErrNotFound) {
		t.Errorf("another person's: %v", err)
	}
	// A key that cannot be read is not sent anywhere.
	calls := p.callCount()
	if _, err := pool.Exec(ctx, `UPDATE connection SET ciphertext = set_byte(ciphertext, 20, get_byte(ciphertext, 20) # 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Recheck(ctx, ann, catalog.OpenAI); !errors.Is(err, ErrUnreadable) {
		t.Errorf("unreadable: %v", err)
	}
	if p.callCount() != calls {
		t.Error("an unreadable key reached the provider")
	}
	assertNoSecret(t, logs.String(), annOpenAI, kek)
}

// providerFunc runs an adapter's part: it takes the key from JobKey, the way
// the OpenRouter adapter does, then answers as the test says.
type providerFunc struct {
	svc    *Service
	submit func(ctx context.Context, key string) (jobs.Submission, error)
	poll   func(ctx context.Context, key string) (jobs.Outcome, error)
}

func (f providerFunc) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	key, err := f.svc.JobKey(ctx, j)
	if err != nil {
		return jobs.Submission{}, err
	}
	return f.submit(ctx, key)
}

func (f providerFunc) Poll(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	key, err := f.svc.JobKey(ctx, j)
	if err != nil {
		return jobs.Outcome{}, err
	}
	return f.poll(ctx, key)
}

func status(t *testing.T, pool *pgxpool.Pool, user int64, provider string) (Status, string, bool) {
	t.Helper()
	var s Status
	var class string
	var success bool
	err := pool.QueryRow(context.Background(), `
		SELECT status, COALESCE(last_error_class, ''), last_success_at IS NOT NULL
		FROM connection WHERE user_id = $1 AND provider = $2`, user, provider).Scan(&s, &class, &success)
	if err != nil {
		t.Fatal(err)
	}
	return s, class, success
}

func TestTrackWritesWhatARunShowsToTheConnection(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	kek := testKEK(t, 1)
	p := newProviders(t, annOpenAI, annGemini)
	svc, logs := newService(t, pool, kek, p)
	for provider, key := range map[string]string{catalog.OpenAI: annOpenAI, catalog.Gemini: annGemini} {
		if _, err := svc.Save(ctx, ann, provider, key); err != nil {
			t.Fatal(err)
		}
	}
	var sawKey string
	adapter := &providerFunc{svc: svc}
	tracked := svc.Track(adapter)
	result := jobs.Outcome{Results: []jobs.Result{{ContentType: "image/png", Data: []byte("png")}}}

	// A failure that proves nothing records its class and keeps the status.
	adapter.submit = func(_ context.Context, key string) (jobs.Submission, error) {
		sawKey = key
		return jobs.Submission{}, &jobs.Error{Class: jobs.RateLimited}
	}
	if _, err := tracked.Submit(ctx, job(ann, catalog.OpenAI)); err == nil {
		t.Fatal("the error was swallowed")
	}
	if sawKey != annOpenAI {
		t.Error("the adapter did not get the key")
	}
	if s, class, ok := status(t, pool, ann, "openai"); s != Valid || class != "rate_limited" || ok {
		t.Errorf("after a rate limit: %s %q %v", s, class, ok)
	}

	// A success verifies it and clears the error.
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		return jobs.Submission{Outcome: &result}, nil
	}
	if _, err := tracked.Submit(ctx, job(ann, catalog.OpenAI)); err != nil {
		t.Fatal(err)
	}
	if s, class, ok := status(t, pool, ann, "openai"); s != Verified || class != "" || !ok {
		t.Errorf("after a success: %s %q %v", s, class, ok)
	}
	// The other provider's connection is not touched.
	if s, _, ok := status(t, pool, ann, "gemini"); s != Valid || ok {
		t.Errorf("Gemini changed: %s %v", s, ok)
	}

	// OpenAI wants the organization verified.
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		return jobs.Submission{}, &jobs.Error{Class: jobs.Key, Err: hinted{"needs_verification"}}
	}
	_, _ = tracked.Submit(ctx, job(ann, catalog.OpenAI))
	if s, class, ok := status(t, pool, ann, "openai"); s != NeedsVerification || class != "key" || !ok {
		t.Errorf("after the 403: %s %q %v", s, class, ok)
	}
	// A poll of a background run that succeeded.
	adapter.poll = func(context.Context, string) (jobs.Outcome, error) { return result, nil }
	if _, err := tracked.Poll(ctx, job(ann, catalog.OpenAI)); err != nil {
		t.Fatal(err)
	}
	if s, class, _ := status(t, pool, ann, "openai"); s != Verified || class != "" {
		t.Errorf("after a poll: %s %q", s, class)
	}

	// No credit, with no hint: money is money.
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		return jobs.Submission{}, &jobs.Error{Class: jobs.Money}
	}
	_, _ = tracked.Submit(ctx, job(ann, catalog.OpenAI))
	if s, class, _ := status(t, pool, ann, "openai"); s != NoCredits || class != "money" {
		t.Errorf("after a 402: %s %q", s, class)
	}
	// Gemini's money is not told apart without a hint.
	_, _ = tracked.Submit(ctx, job(ann, catalog.Gemini))
	if s, class, _ := status(t, pool, ann, "gemini"); s != Valid || class != "money" {
		t.Errorf("Gemini after a 429: %s %q", s, class)
	}
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		return jobs.Submission{}, &jobs.Error{Class: jobs.Money, Err: hinted{"needs_billing"}}
	}
	_, _ = tracked.Submit(ctx, job(ann, catalog.Gemini))
	if s, _, _ := status(t, pool, ann, "gemini"); s != NeedsBilling {
		t.Errorf("Gemini with no billing: %s", s)
	}
	// The key is refused: it is not handed out again.
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		return jobs.Submission{}, &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}
	}
	_, _ = tracked.Submit(ctx, job(ann, catalog.OpenAI))
	if s, _, _ := status(t, pool, ann, "openai"); s != Invalid {
		t.Errorf("after a 401: %s", s)
	}
	if _, err := tracked.Submit(ctx, job(ann, catalog.OpenAI)); err == nil {
		t.Error("a run went out on a refused key")
	} else {
		asJobsError(t, err, jobs.Key)
	}

	// Moderation says nothing of the key.
	if _, err := pool.Exec(ctx, `UPDATE connection SET status = 'verified', last_error_class = NULL WHERE provider = 'gemini'`); err != nil {
		t.Fatal(err)
	}
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		return jobs.Submission{}, &jobs.Error{Class: jobs.Moderation}
	}
	_, _ = tracked.Submit(ctx, job(ann, catalog.Gemini))
	if s, class, _ := status(t, pool, ann, "gemini"); s != Verified || class != "" {
		t.Errorf("after moderation: %s %q", s, class)
	}

	// A free run is not about a person's key.
	free := job(ann, catalog.Gemini)
	free.Free = true
	adapter.submit = func(context.Context, string) (jobs.Submission, error) {
		t.Error("a free run reached the person's key")
		return jobs.Submission{}, nil
	}
	_, _ = tracked.Submit(ctx, free)
	assertNoSecret(t, logs.String(), annOpenAI, kek)
}

// The run that used a key reports on that key, not on the one that replaced
// it while the run was out.
func TestAReportDoesNotLandOnAReplacedKey(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	p := newProviders(t, annOpenAI, evesOpenAI)
	svc, _ := newService(t, pool, testKEK(t, 1), p)
	if _, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI); err != nil {
		t.Fatal(err)
	}
	adapter := &providerFunc{svc: svc}
	adapter.submit = func(ctx context.Context, key string) (jobs.Submission, error) {
		// While the request is out the person pastes another key.
		if _, err := svc.Save(ctx, ann, catalog.OpenAI, evesOpenAI); err != nil {
			t.Error(err)
		}
		return jobs.Submission{}, &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}
	}
	_, _ = svc.Track(adapter).Submit(ctx, job(ann, catalog.OpenAI))
	if s, class, _ := status(t, pool, ann, "openai"); s != Valid || class != "" {
		t.Errorf("the new key took the old key's failure: %s %q", s, class)
	}
	if got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI)); err != nil || got != evesOpenAI {
		t.Errorf("JobKey = %q, %v", got, err)
	}
}

// insertRaw puts a connection straight into the table, sealed under kek.
func insertRaw(t *testing.T, pool *pgxpool.Pool, kek *KEK, user int64, provider, key string) string {
	t.Helper()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	env, err := kek.seal(binding{userID: user, provider: provider, connectionID: id}, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `
		INSERT INTO connection (id, user_id, provider, ciphertext, wrapped_dek, kek_version, last4, status)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, 'valid')`,
		id, user, provider, env.ciphertext, env.wrappedDEK, env.kekVersion, key[len(key)-4:])
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type stored struct {
	ciphertext, wrapped []byte
	version             int
}

func snapshot(t *testing.T, pool *pgxpool.Pool) map[string]stored {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id::text, ciphertext, wrapped_dek, kek_version FROM connection`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]stored{}
	for rows.Next() {
		var id string
		var s stored
		if err := rows.Scan(&id, &s.ciphertext, &s.wrapped, &s.version); err != nil {
			t.Fatal(err)
		}
		out[id] = s
	}
	return out
}

func TestRotationRewrapsTheDataKeysOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	both := testKEK(t, 1, 2)
	oldOnly, newOnly := kekOf(both, 1), kekOf(both, 2)
	p := newProviders(t)

	// People's keys, saved when version 1 was the only one.
	old, _ := newService(t, pool, oldOnly, p)
	want := map[int64]string{}
	for i := range 6 {
		user := ann
		provider := Providers[i%len(Providers)]
		if i >= 3 {
			user = bob
		}
		key := fmt.Sprintf("sk-proj-rotation%02d%s", i, strings.Repeat("Zq8", 8))
		want[user*10+int64(i%3)] = key
		insertRaw(t, pool, oldOnly, user, provider, key)
	}
	before := snapshot(t, pool)
	if len(before) != 6 {
		t.Fatalf("rows = %d", len(before))
	}

	// The operator added a line for version 2 and restarted.
	svc, logs := newService(t, pool, both, p)
	res, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res != (RotateResult{Current: 2, Rewrapped: 6}) {
		t.Fatalf("result = %+v", res)
	}
	after := snapshot(t, pool)
	for id, was := range before {
		now := after[id]
		if now.version != 2 {
			t.Errorf("%s: version %d", id, now.version)
		}
		if !bytes.Equal(now.ciphertext, was.ciphertext) {
			t.Errorf("%s: the encrypted key changed: the rotation re-encrypted it", id)
		}
		if bytes.Equal(now.wrapped, was.wrapped) {
			t.Errorf("%s: the wrap did not change", id)
		}
	}

	// Rotation twice is nothing the second time.
	if res, err = svc.Rotate(ctx); err != nil || res != (RotateResult{Current: 2}) {
		t.Errorf("second rotation = %+v, %v", res, err)
	}

	// With the old line removed, the keys are still there.
	fresh, _ := newService(t, pool, newOnly, p)
	n := 0
	rows, err := pool.Query(ctx, `SELECT user_id, provider FROM connection`)
	if err != nil {
		t.Fatal(err)
	}
	type who struct {
		user     int64
		provider string
	}
	var people []who
	for rows.Next() {
		var w who
		if err := rows.Scan(&w.user, &w.provider); err != nil {
			t.Fatal(err)
		}
		people = append(people, w)
	}
	rows.Close()
	for _, w := range people {
		key, err := fresh.JobKey(ctx, job(w.user, w.provider))
		if err != nil || !strings.HasPrefix(key, "sk-proj-rotation") {
			t.Errorf("%d at %s after the old line went: %q, %v", w.user, w.provider, key, err)
		}
		n++
	}
	if n != 6 {
		t.Errorf("read %d keys", n)
	}
	// The old KEK alone no longer opens them, as it should not.
	if _, err := old.JobKey(ctx, job(ann, Providers[0])); err == nil {
		t.Error("the old KEK opens a rotated key")
	}
	assertNoSecret(t, logs.String(), "sk-proj-rotation00"+strings.Repeat("Zq8", 8), both)
}

func TestRotationLeavesWhatItCannotReadAlone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	both := testKEK(t, 1, 2)
	lost := testKEK(t, 7)
	p := newProviders(t)
	svc, logs := newService(t, pool, both, p)

	good := insertRaw(t, pool, kekOf(both, 1), ann, "openai", annOpenAI)
	// Wrapped under a version the file does not have.
	insertRaw(t, pool, lost, ann, "gemini", annGemini)
	// Altered in place: the wrap no longer authenticates.
	altered := insertRaw(t, pool, kekOf(both, 1), bob, "openai", bobOpenAI)
	if _, err := pool.Exec(ctx, `UPDATE connection SET wrapped_dek = set_byte(wrapped_dek, 30, get_byte(wrapped_dek, 30) # 1) WHERE id = $1::uuid`, altered); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, pool)

	res, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res != (RotateResult{Current: 2, Rewrapped: 1, Failed: 1, Unreadable: 1}) {
		t.Fatalf("result = %+v", res)
	}
	after := snapshot(t, pool)
	if after[good].version != 2 {
		t.Error("the good row was not rotated")
	}
	for id, was := range before {
		if id == good {
			continue
		}
		now := after[id]
		if now.version != was.version || !bytes.Equal(now.wrapped, was.wrapped) || !bytes.Equal(now.ciphertext, was.ciphertext) {
			t.Errorf("%s was changed", id)
		}
	}
	if got, err := svc.JobKey(ctx, job(ann, catalog.OpenAI)); err != nil || got != annOpenAI {
		t.Errorf("the rotated key: %q, %v", got, err)
	}
	_, err = svc.JobKey(ctx, job(ann, catalog.Gemini))
	asJobsError(t, err, jobs.Key)
	if !strings.Contains(logs.String(), "could not be rewrapped") {
		t.Error("the failure was not logged")
	}
	assertNoSecret(t, logs.String(), annOpenAI, both)
}

// More rows than one batch, and two rotations at once: every row is moved
// once and none is lost.
func TestRotationBatchesAndRaces(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	both := testKEK(t, 1, 2)
	old := kekOf(both, 1)
	p := newProviders(t)

	if _, err := pool.Exec(ctx, `INSERT INTO core.person (telegram_user_id) SELECT g FROM generate_series(6000, 6000 + $1 - 1) g ON CONFLICT DO NOTHING`, 3*rotateBatch/2+7); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM core.person WHERE telegram_user_id >= 6000 AND telegram_user_id < 7000`)
	})
	const total = 3*rotateBatch/2 + 7
	for i := range total {
		insertRaw(t, pool, old, int64(6000+i), "openai", fmt.Sprintf("sk-proj-batch%04d%s", i, strings.Repeat("Qw9", 8)))
	}
	a, _ := newService(t, pool, both, p)
	b, _ := newService(t, pool, both, p)
	var wg sync.WaitGroup
	results := make([]RotateResult, 2)
	errs := make([]error, 2)
	for i, svc := range []*Service{a, b} {
		wg.Go(func() { results[i], errs[i] = svc.Rotate(ctx) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if sum := results[0].Rewrapped + results[1].Rewrapped; sum != total {
		t.Errorf("rewrapped %d + %d, want %d once", results[0].Rewrapped, results[1].Rewrapped, total)
	}
	if results[0].Failed+results[1].Failed != 0 {
		t.Errorf("failures: %+v", results)
	}
	for id, s := range snapshot(t, pool) {
		if s.version != 2 {
			t.Fatalf("%s is still on version %d", id, s.version)
		}
	}
	only2, _ := newService(t, pool, kekOf(both, 2), p)
	for _, i := range []int{0, rotateBatch - 1, rotateBatch, total - 1} {
		key, err := only2.JobKey(ctx, job(int64(6000+i), "openai"))
		if want := fmt.Sprintf("sk-proj-batch%04d%s", i, strings.Repeat("Qw9", 8)); err != nil || key != want {
			t.Errorf("row %d: %q, %v", i, key, err)
		}
	}
}

// A person leaving takes their keys with them.
func TestDeletingAPersonDeletesTheirKeys(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	p := newProviders(t, annOpenAI, bobOpenAI)
	svc, _ := newService(t, pool, testKEK(t, 1), p)
	if _, err := svc.Save(ctx, ann, catalog.OpenAI, annOpenAI); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, bob, catalog.OpenAI, bobOpenAI); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM core.person WHERE telegram_user_id = $1`, ann); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.List(ctx, ann); len(list) != 0 {
		t.Error("ann's key survived her")
	}
	if list, _ := svc.List(ctx, bob); len(list) != 1 {
		t.Error("bob's key went too")
	}
}

// Whatever the service logs, in a run through every path, holds no key.
func TestNothingItLogsHoldsAKey(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	kek := testKEK(t, 1, 2)
	p := newProviders(t, annOpenAI, annGemini, annRouter)
	svc, logs := newService(t, pool, kek, p)
	for provider, key := range map[string]string{catalog.OpenAI: annOpenAI, catalog.Gemini: annGemini, catalog.OpenRouter: annRouter} {
		if _, err := svc.Save(ctx, ann, provider, key); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = svc.Save(ctx, bob, catalog.OpenAI, bobOpenAI)
	_, _ = svc.Recheck(ctx, ann, catalog.OpenAI)
	_, _ = svc.JobKey(ctx, job(ann, catalog.OpenAI))
	_, _ = svc.JobKey(ctx, job(bob, catalog.OpenAI))
	if _, err := svc.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	_ = svc.Delete(ctx, ann, catalog.Gemini)
	if !strings.Contains(logs.String(), "key saved") {
		t.Fatalf("the test logged nothing: %q", logs.String())
	}
	for _, key := range []string{annOpenAI, annGemini, annRouter, bobOpenAI} {
		assertNoSecret(t, logs.String(), key, kek)
	}
}
