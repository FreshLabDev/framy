// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The allowance needs a real PostgreSQL, for its locks and its counts:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/free/
package free

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/metrics"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/migrations"
)

const (
	alice = int64(1001)
	bob   = int64(1002)
	carol = int64(1003)
)

// people are the Telegram accounts the tests use: 1001 to 1040.
const people = 40

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("FRAMY_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set FRAMY_TEST_DATABASE_URL to run the database tests")
	}
	// The tests truncate what they touch: never anything but scratch.
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
	if err := store.Migrate(ctx, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stmts := []string{
		`TRUNCATE framy.job_input, framy.asset, framy.job, framy.river_job`,
		`UPDATE framy.runtime_state SET free_budget_alert_day = NULL, free_key_alert_day = NULL`,
		fmt.Sprintf(`INSERT INTO core.person (telegram_user_id) SELECT 1000 + n FROM generate_series(1, %d) n ON CONFLICT DO NOTHING`, people),
		`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ('test/image', 'image', 'Test image', 'test') ON CONFLICT DO NOTHING`,
		`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, fetched_at)
		   VALUES ('openrouter:test/image', 'test/image', 'openrouter', 'test/image', 'Test image', now()) ON CONFLICT DO NOTHING`,
	}
	for _, p := range plans {
		stmts = append(stmts,
			fmt.Sprintf(`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ('%s', '%s', '%s', 'test') ON CONFLICT DO NOTHING`, p.Model, p.Kind, p.Model),
			fmt.Sprintf(`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, fetched_at)
			   VALUES ('%s', '%s', 'openrouter', '%s', '%s', now()) ON CONFLICT DO NOTHING`, p.Offer, p.Model, p.Model, p.Model))
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return pool
}

// harness is the allowance on a real queue, with a clock the tests move and
// an operator chat that remembers what it was told.
type harness struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *jobs.Queue
	a    *Allowance
	key  *fakeGauge

	mu        sync.Mutex
	off       time.Duration
	told      []string
	attempts  int
	notifyErr error
}

func newHarness(t *testing.T, budgetUSD float64) *harness {
	t.Helper()
	h := &harness{t: t, pool: testPool(t), key: &fakeGauge{}}
	h.a = h.allowance(budgetUSD, h.notify)
	q, err := jobs.NewQueue(h.pool, jobs.Config{Admit: h.a, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	h.q = q
	t.Cleanup(h.a.Wait)
	return h
}

func (h *harness) allowance(budgetUSD float64, notify func(context.Context, string) error) *Allowance {
	a, err := New(Config{Pool: h.pool, DailyBudgetUSD: budgetUSD, Key: h.key, Notify: notify, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: h.clock})
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().Add(h.off)
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.off += d
}

func (h *harness) notify(_ context.Context, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts++
	if h.notifyErr != nil {
		return h.notifyErr
	}
	h.told = append(h.told, text)
	return nil
}

func (h *harness) setNotifyErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifyErr = err
}

// notices waits for the notices in flight and returns what the chat heard
// and how many times it was tried.
func (h *harness) notices() (told []string, attempts int) {
	h.a.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.told...), h.attempts
}

// day is the start of the UTC day the allowance's clock is in.
func (h *harness) day() time.Time {
	n := h.clock().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

type fakeGauge struct {
	mu sync.Mutex
	st KeyState
	ok bool
}

func (g *fakeGauge) set(st KeyState, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.st, g.ok = st, ok
}

func (g *fakeGauge) State() (KeyState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.st, g.ok
}

func offerOf(p Plan) catalog.Offer {
	return catalog.Offer{ID: p.Offer, ModelID: p.Model, Provider: catalog.OpenRouter, ProviderModelID: p.Model, Kind: p.Kind}
}

// newJob is a free run of the plan's own, as a surface would build it.
func newJob(user int64, p Plan, estimate float64) jobs.New {
	return jobs.New{
		UserID: user, Surface: jobs.DM, ChatID: user, Offer: offerOf(p), Prompt: "a fox in the snow",
		Settings: p.Settings(), Free: true, Estimate: &price.Estimate{USD: estimate},
	}
}

func (h *harness) enqueue(user int64, p Plan, estimate float64) (int64, bool, error) {
	return h.q.Enqueue(context.Background(), newJob(user, p, estimate))
}

// mustEnqueue enqueues a free run that has to be allowed.
func (h *harness) mustEnqueue(user int64, p Plan, estimate float64) int64 {
	h.t.Helper()
	id, created, err := h.enqueue(user, p, estimate)
	if err != nil || !created {
		h.t.Fatalf("enqueue for %d: %d %v %v", user, id, created, err)
	}
	return id
}

// row is a job written straight into the table, with the state and the time
// the test needs.
type row struct {
	user int64
	plan Plan
	// status is done unless said; class is its error class.
	status, class string
	surface       string
	// ago is how long before the clock the run was queued, or at is the
	// moment itself.
	ago time.Duration
	at  time.Time
	// finishedAgo is how long before the clock it ended; a minute after it
	// was queued by default.
	finishedAgo time.Duration
	// estimate is 0.0195 unless said; actual is NULL unless set.
	estimate float64
	actual   *float64
	// own marks a run on the person's own key.
	own bool
}

func ptr(f float64) *float64 { return &f }

func (h *harness) insert(r row) (id int64, queuedAt time.Time) {
	h.t.Helper()
	if r.status == "" {
		r.status = "done"
	}
	if r.surface == "" {
		r.surface = "dm"
	}
	if r.estimate == 0 {
		r.estimate = 0.0195
	}
	queuedAt = h.clock().Add(-r.ago).Truncate(time.Microsecond)
	if !r.at.IsZero() {
		queuedAt = r.at
	}
	var queued, finished any
	if r.status != "draft" {
		queued = queuedAt
	}
	switch r.status {
	case "done", "failed", "cancelled", "expired":
		if r.finishedAgo != 0 {
			finished = h.clock().Add(-r.finishedAgo).Truncate(time.Microsecond)
		} else {
			finished = queuedAt.Add(time.Minute)
		}
	}
	err := h.pool.QueryRow(context.Background(), `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, free, status, queued_at, finished_at,
		                       estimate_usd, actual_usd, error_class)
		VALUES ($1, $2, $3, $4, 'openrouter', $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''))
		RETURNING id`,
		r.user, r.surface, r.plan.Offer, r.plan.Model, r.plan.Kind, !r.own, r.status, queued, finished,
		r.estimate, r.actual, r.class).Scan(&id)
	if err != nil {
		h.t.Fatalf("insert %+v: %v", r, err)
	}
	return id, queuedAt
}

func (h *harness) insertN(n int, r row) {
	h.t.Helper()
	for range n {
		h.insert(r)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// metric reads a labelled counter from the process's exposition.
func metric(name, label, value string) int {
	var sb strings.Builder
	metrics.Write(&sb)
	prefix := fmt.Sprintf(`%s{%s="%s"} `, name, label, value)
	for _, line := range strings.Split(sb.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			var n int
			_, _ = fmt.Sscan(rest, &n)
			return n
		}
	}
	return 0
}

func asLimit(t *testing.T, err error) *LimitError {
	t.Helper()
	var e *LimitError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want a *LimitError", err, err)
	}
	return e
}

func asClosed(t *testing.T, err error) *ClosedError {
	t.Helper()
	var e *ClosedError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want a *ClosedError", err, err)
	}
	return e
}

func asNotFree(t *testing.T, err error) *NotFreeError {
	t.Helper()
	var e *NotFreeError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want a *NotFreeError", err, err)
	}
	return e
}
