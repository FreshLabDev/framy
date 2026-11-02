// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The API with a real PostgreSQL: the triggers that number and announce a
// job's changes, the listener, and the whole path from POST /api/jobs through
// River to the last event of a stream.
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/api/
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	pic "image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/r2/r2test"
	"github.com/FreshLabDev/framy/internal/storage"
	"github.com/FreshLabDev/framy/migrations"
)

const (
	freeOffer = "openrouter:microsoft/mai-image-2.6-flash"
	paidOffer = "openrouter:test/image"
)

// pg connects to the scratch database, brings it to the current schema and
// empties what the tests use.
func pg(t *testing.T) (*db.Store, *pgxpool.Pool) {
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
	if err := store.Migrate(ctx, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range []string{
		`TRUNCATE framy.site_delivery, framy.chat_run, framy.job_input, framy.asset, framy.job, framy.river_job, framy.fx_rate, framy.profile`,
		`UPDATE framy.runtime_state SET bucket_alert_step = 0`,
		`INSERT INTO core.person (telegram_user_id) VALUES (1001), (1002) ON CONFLICT DO NOTHING`,
		`INSERT INTO framy.catalog_model (id, kind, name, maker, free) VALUES
		   ('test/image', 'image', 'Test image', 'test', false),
		   ('microsoft/mai-image-2.6-flash', 'image', 'MAI 2.6 Flash', 'Microsoft', true)
		 ON CONFLICT (id) DO UPDATE SET disabled_at = NULL, hidden = NULL`,
		// Both take reference images, as a photo brought in is one.
		`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, input_modalities, params, routes, fetched_at) VALUES
		   ('openrouter:test/image', 'test/image', 'openrouter', 'test/image', 'Test image',
		    '{text,image}', '{"input_references":{"type":"range","min":0,"max":4}}',
		    '[{"prices":[{"billable":"output_image","unit":"image","cost_usd":0.04}]}]', now()),
		   ('openrouter:microsoft/mai-image-2.6-flash', 'microsoft/mai-image-2.6-flash', 'openrouter', 'microsoft/mai-image-2.6-flash', 'MAI 2.6 Flash',
		    '{text,image}', '{"input_references":{"type":"range","min":0,"max":4},"n":{"type":"range","min":1,"max":4},"aspect_ratio":{"type":"enum","values":["1:1","16:9"]}}',
		    '[{"prices":[{"billable":"output_image","unit":"image","cost_usd":0.0195}]}]', now())
		 ON CONFLICT (id) DO UPDATE SET input_modalities = EXCLUDED.input_modalities, params = EXCLUDED.params,
		   routes = EXCLUDED.routes, disabled_at = NULL, hidden = NULL`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return store, pool
}

// quiet is a logger that says nothing.
var quiet = slog.New(slog.DiscardHandler)

// stepProvider is an upstream that holds a run until the test lets it go,
// and keeps the results as assets, as the storage would.
type stepProvider struct {
	pool    *pgxpool.Pool
	release chan struct{}
	mu      sync.Mutex
	started []int64
}

func newStepProvider(pool *pgxpool.Pool) *stepProvider {
	return &stepProvider{pool: pool, release: make(chan struct{})}
}

func (p *stepProvider) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	p.mu.Lock()
	p.started = append(p.started, j.ID)
	p.mu.Unlock()
	select {
	case <-p.release:
	case <-ctx.Done():
		return jobs.Submission{}, ctx.Err()
	}
	cost := 0.0391
	return jobs.Submission{Outcome: &jobs.Outcome{Results: []jobs.Result{{ContentType: "image/png", Data: []byte("png")}}, CostUSD: &cost}}, nil
}

func (p *stepProvider) Poll(context.Context, jobs.Job) (jobs.Outcome, error) {
	return jobs.Outcome{}, errors.New("not used")
}

func (p *stepProvider) Keep(ctx context.Context, j jobs.Job, results []jobs.Result) error {
	for i := range results {
		_, err := p.pool.Exec(ctx, `
			INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, width, height, sha256)
			VALUES ($1, 'result', $2, $3, 'image/png', 3, 1, 1, 'x')
			ON CONFLICT (object_key) DO NOTHING`, j.UserID, j.ID, fmt.Sprintf("u/%d/out/%d-%d.png", j.UserID, j.ID, i))
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *stepProvider) wasStarted(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.started {
		if s == id {
			return true
		}
	}
	return false
}

// world is the API on a real database, with a real queue behind it.
type world struct {
	t     *testing.T
	store *db.Store
	pool  *pgxpool.Pool
	prov  *stepProvider
	queue *jobs.Queue
	conns *fakeConns
	allow *free.Allowance
	// listening counts the times a listener got connected.
	listening atomic.Int64
}

type worldOptions struct {
	budget float64
}

func newWorld(t *testing.T, opts ...func(*worldOptions)) *world {
	t.Helper()
	o := worldOptions{budget: 1}
	for _, f := range opts {
		f(&o)
	}
	store, pool := pg(t)
	w := &world{t: t, store: store, pool: pool, prov: newStepProvider(pool), conns: &fakeConns{}}
	w.conns.list = []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}
	var err error
	if w.allow, err = free.New(free.Config{Pool: pool, DailyBudgetUSD: o.budget, Log: quiet}); err != nil {
		t.Fatal(err)
	}
	w.queue, err = jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openrouter": w.prov}, Sink: w.prov, Admit: w.allow, Log: quiet,
		WaitSlot: 100 * time.Millisecond, PollEvery: 100 * time.Millisecond, StopGrace: 5 * time.Second, FetchPoll: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// startQueue runs River for the rest of the test.
func (w *world) startQueue() {
	w.t.Helper()
	if err := w.queue.Start(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = w.queue.Stop(ctx)
	})
}

// server starts an API instance, as one process of framy would, and a listener
// for it; stopping the instance is cancelling its context.
type instance struct {
	*rig
	stop context.CancelFunc
	done chan struct{}
}

func (w *world) instance(opts ...func(*rigOptions)) *instance {
	w.t.Helper()
	w.listening.Store(0)
	in := &instance{done: make(chan struct{})}
	cfgFn := func(c *Config) {
		c.Pool, c.Queue, c.Store, c.Keys, c.data = w.pool, w.queue, w.store, w.conns, nil
		c.Ping, c.Keepalive, c.Backoff = time.Hour, 100*time.Millisecond, 300*time.Millisecond
		c.listening = func() { w.listening.Add(1) }
		c.Uploads, c.Files = nil, nil
	}
	opts = append([]func(*rigOptions){func(o *rigOptions) {
		prev := o.cfg
		o.cfg = func(c *Config) {
			cfgFn(c)
			if prev != nil {
				prev(c)
			}
		}
	}}, opts...)
	in.rig = newRig(w.t, opts...)
	ctx, cancel := context.WithCancel(context.Background())
	in.stop = cancel
	go func() { in.api.Run(ctx); close(in.done) }()
	w.t.Cleanup(func() { cancel(); <-in.done })
	eventually(w.t, "the listener to connect", func() bool { return w.listening.Load() >= 1 })
	return in
}

// ---- the triggers ----

// notices LISTENs on its own connection and gathers what comes.
type notices struct {
	t    *testing.T
	conn *pgx.Conn
}

func listen(t *testing.T, pool *pgxpool.Pool) *notices {
	t.Helper()
	conn, err := pgx.ConnectConfig(context.Background(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	if _, err := conn.Exec(context.Background(), "LISTEN "+channel); err != nil {
		t.Fatal(err)
	}
	return &notices{t: t, conn: conn}
}

// within returns what arrives in the time, and when it falls quiet.
func (n *notices) within(d time.Duration) []event {
	n.t.Helper()
	var out []event
	deadline := time.Now().Add(d)
	for {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		note, err := n.conn.WaitForNotification(ctx)
		cancel()
		if err != nil {
			return out
		}
		var e event
		if err := json.Unmarshal([]byte(note.Payload), &e); err != nil {
			n.t.Fatalf("a notice that is not JSON: %q", note.Payload)
		}
		if len(note.Payload) > 200 {
			n.t.Errorf("the payload is %d bytes; it should stay small", len(note.Payload))
		}
		out = append(out, e)
	}
}

func seqOf(t *testing.T, pool *pgxpool.Pool, id int64) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(context.Background(), `SELECT event_seq FROM job WHERE id=$1`, id).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestEveryVisibleChangeIsNumberedAndAnnounced(t *testing.T) {
	w := newWorld(t)
	n := listen(t, w.pool)
	ctx := context.Background()

	id, _, err := w.queue.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: catalog.Offer{ID: paidOffer, ModelID: "test/image", Provider: "openrouter", Kind: catalog.Image}, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := n.within(300 * time.Millisecond); len(got) != 1 || got[0] != (event{Job: id, User: alice, Seq: 1, Status: "queued"}) {
		t.Fatalf("a new job: %+v", got)
	}

	// A write that shows nothing new is no event: the provider's id, a retry
	// time, updated_at.
	exec(t, w.pool, `UPDATE job SET provider_job_id='p1', retry_at=now(), updated_at=now() WHERE id=$1`, id)
	if got := n.within(200 * time.Millisecond); len(got) != 0 || seqOf(t, w.pool, id) != 1 {
		t.Fatalf("an invisible change: %+v, seq %d", got, seqOf(t, w.pool, id))
	}

	// Each thing a person sees is one.
	for i, c := range []struct{ sql, status string }{
		{`UPDATE job SET status='running', started_at=now() WHERE id=$1`, "running"},
		{`UPDATE job SET error_class='unavailable', error='503' WHERE id=$1`, "running"},
		{`UPDATE job SET results=1 WHERE id=$1`, "running"},
		{`UPDATE job SET actual_usd=0.03 WHERE id=$1`, "running"},
		{`UPDATE job SET status='done', finished_at=now() WHERE id=$1`, "done"},
	} {
		exec(t, w.pool, c.sql, id)
		got := n.within(300 * time.Millisecond)
		if len(got) != 1 || got[0] != (event{Job: id, User: alice, Seq: int64(i + 2), Status: c.status}) {
			t.Fatalf("%s: %+v", c.sql, got)
		}
	}
}

func TestTheQueuesOwnTransitionsAreAnnounced(t *testing.T) {
	w := newWorld(t)
	n := listen(t, w.pool)
	ctx := context.Background()
	offer := catalog.Offer{ID: paidOffer, ModelID: "test/image", Provider: "openrouter", Kind: catalog.Image}

	draft, _, err := w.queue.Draft(ctx, jobs.New{UserID: alice, Surface: jobs.DM, ChatID: alice, Offer: offer, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Generate(ctx, draft); err != nil {
		t.Fatal(err)
	}
	queued, _, err := w.queue.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: offer, Prompt: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Cancel(ctx, queued); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range n.within(400 * time.Millisecond) {
		got = append(got, fmt.Sprintf("%d:%d:%s", e.Job, e.Seq, e.Status))
	}
	want := []string{
		fmt.Sprintf("%d:1:draft", draft), fmt.Sprintf("%d:2:queued", draft),
		fmt.Sprintf("%d:1:queued", queued), fmt.Sprintf("%d:2:cancelled", queued),
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestACancelledTransactionAnnouncesNothing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id, _, err := w.queue.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: catalog.Offer{ID: paidOffer, ModelID: "test/image", Provider: "openrouter", Kind: catalog.Image}, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	n := listen(t, w.pool)
	n.within(100 * time.Millisecond)

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE job SET status='running', started_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := n.within(150 * time.Millisecond); len(got) != 0 {
		t.Fatalf("a notice came before the transaction ended: %+v", got)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := n.within(150 * time.Millisecond); len(got) != 0 || seqOf(t, w.pool, id) != 1 {
		t.Fatalf("a rolled back change was announced: %+v, seq %d", got, seqOf(t, w.pool, id))
	}

	// Committed, it is announced, and not before.
	tx, _ = w.pool.Begin(ctx)
	tx.Exec(ctx, `UPDATE job SET status='running', started_at=now() WHERE id=$1`, id)
	tx.Exec(ctx, `UPDATE job SET results=2 WHERE id=$1`, id)
	if got := n.within(100 * time.Millisecond); len(got) != 0 {
		t.Fatalf("a notice came before the commit: %+v", got)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := n.within(300 * time.Millisecond)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("%+v", got)
	}
}

// Two workers changing one job at once cannot both leave the same number:
// the second waits for the first's row lock, and its trigger sees the first's
// row.
func TestConcurrentChangesGetDifferentNumbers(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id, _, err := w.queue.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: catalog.Offer{ID: paidOffer, ModelID: "test/image", Provider: "openrouter", Kind: catalog.Image}, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	n := listen(t, w.pool)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 5 {
				if _, err := w.pool.Exec(ctx, `UPDATE job SET results = results + 1 WHERE id=$1`, id); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, e := range n.within(500 * time.Millisecond) {
		if seen[e.Seq] {
			t.Errorf("seq %d announced twice", e.Seq)
		}
		seen[e.Seq] = true
	}
	if seqOf(t, w.pool, id) != 41 || len(seen) != 40 {
		t.Errorf("seq %d after 40 changes, %d announced", seqOf(t, w.pool, id), len(seen))
	}
}

// ---- the whole path ----

// post starts a job over HTTP as a person.
func (in *instance) post(user int64, key, body string, hdr ...string) (int, map[string]any, http.Header) {
	in.t.Helper()
	hdr = append([]string{"Content-Type", "application/json", "Idempotency-Key", key}, hdr...)
	resp := in.do(user, http.MethodPost, "/api/jobs", strings.NewReader(body), hdr...)
	h := resp.Header
	return resp.StatusCode, jsonOf(in.t, resp), h
}

func idOf(t *testing.T, m map[string]any) int64 {
	t.Helper()
	f, ok := m["id"].(float64)
	if !ok {
		t.Fatalf("no job id in %v", m)
	}
	return int64(f)
}

func TestAJobsWholeLifeArrivesOverTheStreamInOrder(t *testing.T) {
	w := newWorld(t)
	in := w.instance()

	status, m, _ := in.post(alice, "life", body(paidOffer, "key"))
	if status != http.StatusAccepted || m["status"] != "queued" {
		t.Fatalf("%d %v", status, m)
	}
	id := idOf(t, m)
	path := fmt.Sprintf("/api/jobs/%d/events", id)
	s := in.openStream(alice, path)
	if id, m := s.event(wait); id != 1 || m["status"] != "queued" {
		t.Fatalf("%d %v", id, m)
	}

	w.startQueue() // a worker claims the job and the provider holds it
	if id, m := s.event(wait); id != 2 || m["status"] != "running" {
		t.Fatalf("%d %v", id, m)
	}
	s.quiet(150 * time.Millisecond)

	close(w.prov.release)
	evID, m := s.event(wait)
	if evID != 3 || m["status"] != "done" {
		t.Fatalf("%d %v", evID, m)
	}
	cost := m["cost"].(map[string]any)
	res := m["results"].([]any)
	if cost["usd"] != 0.0391 || cost["label"] != "$0.04" || len(res) != 1 || res[0].(map[string]any)["content_type"] != "image/png" {
		t.Errorf("%v", m)
	}
	if !s.ended(wait) {
		t.Error("the stream should end after the final state")
	}

	// Polling says the same.
	polled := jsonOf(t, in.do(alice, "GET", fmt.Sprintf("/api/jobs/%d", id), nil))
	delete(m, "results")
	delete(polled, "results")
	if fmt.Sprint(polled) != fmt.Sprint(m) {
		t.Errorf("polling:\n%v\nthe stream:\n%v", polled, m)
	}
}

func TestAStreamOpenedLateOrResumedMissesNothing(t *testing.T) {
	w := newWorld(t)
	w.startQueue()
	close(w.prov.release)
	in := w.instance()
	_, m, _ := in.post(alice, "late", body(paidOffer, "key"))
	id := idOf(t, m)
	path := fmt.Sprintf("/api/jobs/%d/events", id)
	eventually(t, "the job to end", func() bool { return seqOf(t, w.pool, id) == 3 })

	// Late: the end, and nothing of what led to it.
	s := in.openStream(alice, path)
	if evID, m := s.event(wait); evID != 3 || m["status"] != "done" || len(m["results"].([]any)) != 1 {
		t.Fatalf("%d %v", evID, m)
	}
	if !s.ended(wait) {
		t.Error("the stream should end")
	}
	// Resumed from any id before the end: the end, once.
	for _, last := range []string{"1", "2"} {
		s = in.openStream(alice, path, "Last-Event-ID", last)
		if evID, _ := s.event(wait); evID != 3 {
			t.Errorf("from %s: event %d", last, evID)
		}
		if !s.ended(wait) {
			t.Errorf("from %s: the stream should end", last)
		}
	}
	// From the end: nothing, and 204 so that EventSource stops.
	if s = in.openStream(alice, path, "Last-Event-ID", "3"); s.resp.StatusCode != http.StatusNoContent {
		t.Errorf("%d", s.resp.StatusCode)
	}
}

func TestARestartLosesNoResult(t *testing.T) {
	w := newWorld(t)
	w.startQueue()
	first := w.instance()
	_, m, _ := first.post(alice, "restart", body(paidOffer, "key"))
	id := idOf(t, m)
	path := fmt.Sprintf("/api/jobs/%d/events", id)
	s := first.openStream(alice, path)
	var last int64
	for {
		evID, m := s.event(wait)
		last = evID
		if m["status"] == "running" {
			break
		}
	}

	// The process stops: its streams end, and the job goes on in River's
	// hands, the run finishing while nobody is listening.
	first.stop()
	<-first.done
	if !s.ended(wait) {
		t.Fatal("a stream outlived its process")
	}
	close(w.prov.release)
	eventually(t, "the job to end", func() bool { return seqOf(t, w.pool, id) == last+1 })

	// The next process: the client reconnects with the id it had.
	second := w.instance()
	s = second.openStream(alice, path, "Last-Event-ID", strconv.FormatInt(last, 10))
	evID, m := s.event(wait)
	if evID != last+1 || m["status"] != "done" || len(m["results"].([]any)) != 1 {
		t.Fatalf("%d %v", evID, m)
	}
}

func TestAListenerDropAndReconnectLosesNothing(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	ctx := context.Background()

	_, m, _ := in.post(alice, "drop", body(paidOffer, "key"))
	id := idOf(t, m)
	s := in.openStream(alice, fmt.Sprintf("/api/jobs/%d/events", id))
	s.event(wait)

	// A notice reaches the stream while the listener is up.
	exec(t, w.pool, `UPDATE job SET status='running', started_at=now() WHERE id=$1`, id)
	if evID, m := s.event(wait); evID != 2 || m["status"] != "running" {
		t.Fatalf("%d %v", evID, m)
	}

	// The connection is cut, and the next changes come while nobody listens.
	reconnects := w.listening.Load()
	exec(t, w.pool, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'framy-events' AND datname = current_database()`)
	eventually(t, "the listener's backend to be gone", func() bool {
		var n int
		w.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name = 'framy-events' AND datname = current_database()`).Scan(&n)
		return n == 0
	})
	exec(t, w.pool, `UPDATE job SET results=1 WHERE id=$1`, id)
	exec(t, w.pool, `UPDATE job SET status='done', finished_at=now() WHERE id=$1`, id)

	eventually(t, "the listener to connect again", func() bool { return w.listening.Load() > reconnects })
	// The stream's ping is an hour: only the reconnect can have told it.
	evID, m := s.event(wait)
	if evID != 4 || m["status"] != "done" {
		t.Fatalf("%d %v", evID, m)
	}
	if !s.ended(wait) {
		t.Error("the stream should end")
	}
}

func TestAnotherPersonsJobIsNotFoundEverywhere(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	_, m, _ := in.post(alice, "private", body(paidOffer, "key"))
	id := idOf(t, m)
	missing := bodyOf(t, in.do(bob, "GET", "/api/jobs/9999999", nil))
	for _, path := range []string{fmt.Sprintf("/api/jobs/%d", id), fmt.Sprintf("/api/jobs/%d/events", id)} {
		resp := in.do(bob, "GET", path, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
		if got := bodyOf(t, resp); got != missing {
			t.Errorf("%s: %s", path, got)
		}
	}
	if resp := in.do(alice, "GET", fmt.Sprintf("/api/jobs/%d", id), nil); resp.StatusCode != http.StatusOK {
		t.Errorf("the owner: %d", resp.StatusCode)
	}
}

// A failed job's message is the provider's words when it said any, without
// framy's line around them; otherwise framy's line, as before.
func TestAFailedJobSaysWhatTheProviderSaid(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	_, m, _ := in.post(alice, "faces", body(paidOffer, "key"))
	id := idOf(t, m)
	exec(t, w.pool, `UPDATE job SET status='failed', finished_at=now(), error_class='faces',
		error='faces: openrouter 400 InputVideoSensitiveContentDetected.PrivacyInformation at BytePlus: The input video may contain real person.',
		provider_message='The input video may contain real person.' WHERE id=$1`, id)
	path := fmt.Sprintf("/api/jobs/%d", id)
	got := jsonOf(t, in.do(alice, "GET", path, nil))["error"].(map[string]any)
	if got["class"] != "faces" || got["message"] != "The input video may contain real person." {
		t.Fatalf("error = %v", got)
	}
	exec(t, w.pool, `UPDATE job SET error_class='invalid_request', error='invalid_request: fast is not a setting', provider_message=NULL WHERE id=$1`, id)
	got = jsonOf(t, in.do(alice, "GET", path, nil))["error"].(map[string]any)
	if got["class"] != "invalid_request" || got["message"] != "invalid_request: fast is not a setting" {
		t.Fatalf("error = %v", got)
	}
}

func TestARepeatedPostStartsOneJob(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	req := body(paidOffer, "key")
	var wg sync.WaitGroup
	ids := make([]int64, 6)
	for i := range ids {
		wg.Go(func() {
			status, m, _ := in.post(alice, "double-click", req)
			if status != http.StatusAccepted {
				t.Errorf("%d %v", status, m)
				return
			}
			ids[i] = idOf(t, m)
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids %v", ids)
		}
	}
	var jobsN, riverN int
	w.pool.QueryRow(context.Background(), `SELECT count(*) FROM job`).Scan(&jobsN)
	w.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job`).Scan(&riverN)
	if jobsN != 1 || riverN != 1 {
		t.Errorf("%d jobs, %d River jobs", jobsN, riverN)
	}
	// The key is theirs alone.
	_, m, _ := in.post(bob, "double-click", req)
	if idOf(t, m) == ids[0] {
		t.Error("two people with the same key share a job")
	}
}

func TestTheFreeAllowanceSpeaksThroughTheAPI(t *testing.T) {
	w := newWorld(t, func(o *worldOptions) { o.budget = 0.05 })
	in := w.instance()
	freeBody := func(extra ...string) string { return body(freeOffer, "free", extra...) }

	// The first runs; its settings are the allowance's, put there by the API.
	status, m, _ := in.post(alice, "f1", freeBody())
	if status != http.StatusAccepted || m["free"] != true {
		t.Fatalf("%d %v", status, m)
	}
	var settings string
	w.pool.QueryRow(context.Background(), `SELECT settings::text FROM job WHERE id=$1`, idOf(t, m)).Scan(&settings)
	if settings != `{"n": 1}` {
		t.Errorf("settings %s", settings)
	}

	// Another setting, a model off the list, and a paid offer are not free.
	for name, b := range map[string]string{
		"a locked setting": freeBody(`"settings":{"aspect_ratio":"16:9"}`),
		"a locked value":   freeBody(`"settings":{"n":4}`),
		"another model":    body(paidOffer, "free"),
	} {
		status, m, _ := in.post(alice, "f-"+strings.ReplaceAll(name, " ", "-"), b)
		if status != http.StatusUnprocessableEntity || m["error"] != "not_free" || m["reason"] == "" {
			t.Errorf("%s: %d %v", name, status, m)
		}
	}

	// The day's budget is $0.05: two runs of $0.0195 fit, the third closes it.
	if status, m, _ := in.post(alice, "f2", freeBody()); status != http.StatusAccepted {
		t.Fatalf("%d %v", status, m)
	}
	status, m, h := in.post(alice, "f3", freeBody())
	if status != http.StatusServiceUnavailable || m["error"] != "free_closed" || m["until"] == nil || h.Get("Retry-After") == "" {
		t.Fatalf("%d %v", status, m)
	}
	if until, err := time.Parse(time.RFC3339, m["until"].(string)); err != nil || until.UTC().Hour() != 0 || !until.After(time.Now()) {
		t.Errorf("until %v", m["until"])
	}
}

func TestThePersonsFreeLimitHasATimeToComeBack(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	for i := range free.ImagesPerWindow {
		if status, m, _ := in.post(alice, fmt.Sprintf("l%d", i), body(freeOffer, "free")); status != http.StatusAccepted {
			t.Fatalf("run %d: %d %v", i, status, m)
		}
	}
	status, m, h := in.post(alice, "l-over", body(freeOffer, "free"))
	if status != http.StatusTooManyRequests || m["error"] != "free_limit" || m["kind"] != "image" || m["limit"] != float64(5) || m["window_seconds"] != float64(86400) {
		t.Fatalf("%d %v", status, m)
	}
	freesAt, err := time.Parse(time.RFC3339, m["frees_at"].(string))
	if err != nil || time.Until(freesAt) < 23*time.Hour || time.Until(freesAt) > 25*time.Hour {
		t.Errorf("frees_at %v", m["frees_at"])
	}
	retry, _ := strconv.Atoi(h.Get("Retry-After"))
	if retry < 23*3600 || retry > 25*3600 {
		t.Errorf("Retry-After %q", h.Get("Retry-After"))
	}
	// It is the person's own: another one runs.
	if status, m, _ := in.post(bob, "l-bob", body(freeOffer, "free")); status != http.StatusAccepted {
		t.Errorf("another person: %d %v", status, m)
	}
	// A run on their own key is not the allowance's.
	if status, m, _ := in.post(alice, "l-key", body(paidOffer, "key")); status != http.StatusAccepted {
		t.Errorf("a run on their own key: %d %v", status, m)
	}
}

func TestACurrencyIsKeptWithTheRateItWasShownIn(t *testing.T) {
	w := newWorld(t)
	day := time.Now().UTC().Format(time.DateOnly)
	exec(t, w.pool, `INSERT INTO fx_rate (date, code, uah_per_unit, source) VALUES ($1, 'USD', 41.5, 'nbu'), ($1, 'EUR', 48.25, 'nbu')`, day)
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	stranger := w.instance()
	proxied := w.instance(func(o *rigOptions) { o.proxies = trusted })

	row := func(id int64) (cur *string, rate *float64) {
		w.pool.QueryRow(context.Background(), `SELECT currency, fx_rate::float8 FROM job WHERE id=$1`, id).Scan(&cur, &rate)
		return
	}
	// Behind the proxy the country counts.
	status, m, _ := proxied.post(alice, "c1", body(paidOffer, "key"), "CF-IPCountry", "UA")
	est := m["estimate"].(map[string]any)
	if status != 202 || est["currency"] != "UAH" || est["rate_date"] != day {
		t.Fatalf("%d %v", status, m)
	}
	if cur, rate := row(idOf(t, m)); cur == nil || *cur != "UAH" || rate == nil || *rate != 41.5 {
		t.Errorf("the job keeps %v %v", cur, rate)
	}
	// From anyone else the header is only a header.
	_, m, _ = stranger.post(alice, "c2", body(paidOffer, "key"), "CF-IPCountry", "UA")
	if est := m["estimate"].(map[string]any); est["currency"] != "USD" {
		t.Errorf("%v", est)
	}
	if cur, rate := row(idOf(t, m)); cur != nil || rate != nil {
		t.Errorf("a price in dollars keeps no rate: %v %v", cur, rate)
	}
	// What the person chose beats the country, here and there.
	if err := w.store.SetCurrency(context.Background(), alice, "EUR"); err != nil {
		t.Fatal(err)
	}
	_, m, _ = proxied.post(alice, "c3", body(paidOffer, "key"), "CF-IPCountry", "UA")
	if est := m["estimate"].(map[string]any); est["currency"] != "EUR" {
		t.Errorf("%v", est)
	}
	_, m, _ = stranger.post(alice, "c4", body(paidOffer, "key"), "CF-IPCountry", "UA")
	if est := m["estimate"].(map[string]any); est["currency"] != "EUR" {
		t.Errorf("%v", est)
	}
	// A currency with no rate saved yet is shown in dollars.
	if err := w.store.SetCurrency(context.Background(), alice, "PLN"); err != nil {
		t.Fatal(err)
	}
	_, m, _ = proxied.post(alice, "c5", body(paidOffer, "key"))
	if est := m["estimate"].(map[string]any); est["currency"] != "USD" {
		t.Errorf("%v", est)
	}
	// The language of core, when nothing else says.
	exec(t, w.pool, `SELECT core.set_language('framy', 'user', $1, 'uk', 'manual')`, bob)
	_, m, _ = proxied.post(bob, "c6", body(paidOffer, "key"))
	if est := m["estimate"].(map[string]any); est["currency"] != "UAH" || est["label"] != "1,66 ₴" {
		t.Errorf("%v", est)
	}
}

func TestAnUnavailableOfferIsRefused(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	exec(t, w.pool, `UPDATE catalog_offer SET disabled_at = now() WHERE id = $1`, paidOffer)
	status, m, _ := in.post(alice, "gone", body(paidOffer, "key"))
	if status != http.StatusUnprocessableEntity || m["error"] != "unknown_offer" {
		t.Errorf("a disabled offer: %d %v", status, m)
	}
	exec(t, w.pool, `UPDATE catalog_offer SET disabled_at = NULL, hidden = 'leaving soon' WHERE id = $1`, paidOffer)
	status, m, _ = in.post(alice, "hidden", body(paidOffer, "key"))
	if status != http.StatusUnprocessableEntity || m["error"] != "unknown_offer" {
		t.Errorf("a hidden offer: %d %v", status, m)
	}
	exec(t, w.pool, `UPDATE catalog_offer SET hidden = NULL WHERE id = $1`, paidOffer)
}

// A server that cannot finish its shutdown because of a stream would end
// every deploy with an error: Run's end ends the streams.
func TestStoppingTheListenerEndsTheStreams(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	_, m, _ := in.post(alice, "stop", body(paidOffer, "key"))
	s := in.openStream(alice, fmt.Sprintf("/api/jobs/%d/events", idOf(t, m)))
	s.event(wait)
	in.stop()
	<-in.done
	if !s.ended(wait) {
		t.Error("an open stream would hold up the server's shutdown")
	}
	// The listener's own connection went with it.
	var n int
	w.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE application_name = 'framy-events' AND datname = current_database()`).Scan(&n)
	if n != 0 {
		eventually(t, "the listener's connection to close", func() bool {
			w.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE application_name = 'framy-events' AND datname = current_database()`).Scan(&n)
			return n == 0
		})
	}
}

// aPNG is a small picture to bring.
func aPNG(t *testing.T) []byte {
	t.Helper()
	img := pic.NewRGBA(pic.Rect(0, 0, 64, 48))
	for x := 0; x < 64; x++ {
		for y := 0; y < 48; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 5), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAPhotoBroughtOverHTTPIsAJobInputOfItsOwnerAlone(t *testing.T) {
	conv := &media.Converter{}
	if err := conv.Check(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot run the media tests here: %v", err)
	}
	w := newWorld(t)
	bucket := r2test.New(t)
	files, err := storage.New(storage.Config{
		Pool: w.pool, Bucket: bucket.Client(t), Media: conv, Work: media.Work{Root: filepath.Join(t.TempDir(), "work")},
		Fetch: http.DefaultClient, Log: quiet,
		Notify: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	in := w.instance(func(o *rigOptions) {
		prev := o.cfg
		o.cfg = func(c *Config) {
			if prev != nil {
				prev(c)
			}
			c.Uploads, c.Links = files, files
		}
	})

	resp := in.do(alice, http.MethodPost, "/api/uploads", bytes.NewReader(aPNG(t)), "Content-Type", "image/png")
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusCreated || m["content_type"] == "" || m["width"] != float64(64) || m["height"] != float64(48) {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	asset := int64(m["id"].(float64))
	if got := len(bucket.Keys()); got != 1 {
		t.Fatalf("%d objects in the bucket after one upload", got)
	}

	// The photo goes in with a job, and the job keeps the order.
	in2 := fmt.Sprintf(`{"offer":%q,"prompt":"p","pay":"key","inputs":[%d]}`, paidOffer, asset)
	status, job, _ := in.post(alice, "photo", in2)
	if status != http.StatusAccepted {
		t.Fatalf("%d %v", status, job)
	}
	var linked int64
	if err := w.pool.QueryRow(context.Background(), `SELECT asset_id FROM job_input WHERE job_id = $1 AND position = 0`, idOf(t, job)).Scan(&linked); err != nil || linked != asset {
		t.Fatalf("job_input: %d, %v", linked, err)
	}

	// Another person cannot use it, and no job is made.
	status, refused, _ := in.post(bob, "photo-of-alices", in2)
	if status != http.StatusUnprocessableEntity || refused["error"] != "unknown_input" {
		t.Errorf("%d %v", status, refused)
	}
	var jobs int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE user_id = $1`, bob).Scan(&jobs); err != nil || jobs != 0 {
		t.Errorf("%d jobs of bob, %v", jobs, err)
	}

	// Not a picture: turned away before anything is kept.
	resp = in.do(alice, http.MethodPost, "/api/uploads", strings.NewReader("this is text, not a picture"), "Content-Type", "image/png")
	m = jsonOf(t, resp)
	if resp.StatusCode != http.StatusUnsupportedMediaType || m["error"] != "unsupported_type" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
	if got := len(bucket.Keys()); got != 1 {
		t.Errorf("%d objects in the bucket after a refused upload", got)
	}

	// The finished job links its result through the bucket's signature.
	w.startQueue()
	close(w.prov.release)
	s := in.openStream(alice, fmt.Sprintf("/api/jobs/%d/events", idOf(t, job)))
	for {
		_, ev := s.event(wait)
		if ev["status"] == "done" {
			res := ev["results"].([]any)
			link, _ := res[0].(map[string]any)["url"].(string)
			if !strings.HasPrefix(link, bucket.URL) {
				t.Errorf("a result link should point at the bucket")
			}
			// Its download link, signed by the bucket too, saves it as a
			// file of the job.
			dl, _ := res[0].(map[string]any)["download"].(string)
			u, err := url.Parse(dl)
			if err != nil {
				t.Fatal("the download link does not parse")
			}
			want := fmt.Sprintf(`attachment; filename="framy-%d-1.`, idOf(t, job))
			if d := u.Query().Get("response-content-disposition"); !strings.HasPrefix(dl, bucket.URL) || !strings.HasPrefix(d, want) {
				t.Errorf("download saves as %q, want %s...", d, want)
			}
			break
		}
	}
}

func TestTheComposersReadsComeFromTheDatabase(t *testing.T) {
	w := newWorld(t)
	in := w.instance(func(o *rigOptions) {
		prev := o.cfg
		o.cfg = func(c *Config) {
			if prev != nil {
				prev(c)
			}
			c.Allowance = w.allow
		}
	})
	exec(t, w.pool, `UPDATE framy.catalog_offer SET routes = '[{"prices":[{"billable":"output_image","unit":"image","cost_usd":0.04}],"discount":0.25}]' WHERE id = $1`, paidOffer)

	// The catalog is the database's, for anyone.
	resp := in.do(0, http.MethodGet, "/api/catalog", nil)
	cat := jsonOf(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, cat)
	}
	seen := map[string]bool{}
	for _, m := range cat["models"].([]any) {
		for _, of := range m.(map[string]any)["offers"].([]any) {
			of := of.(map[string]any)
			seen[of["id"].(string)] = true
			if of["id"] == paidOffer && of["discount"] != 0.25 {
				t.Errorf("paid offer = %v", of)
			}
		}
	}
	if !seen[freeOffer] || !seen[paidOffer] {
		t.Fatalf("offers %v", seen)
	}

	quote := func() map[string]any {
		resp := in.do(alice, http.MethodPost, "/api/quote", strings.NewReader(
			`{"items":[{"offer":"`+freeOffer+`","pay":"free"},{"offer":"`+paidOffer+`","pay":"key"}]}`), "Content-Type", "application/json")
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %v", resp.StatusCode, m)
		}
		return m
	}
	it := items(t, quote())
	if it[0]["refusal"] != nil || it[1]["refusal"] != nil {
		t.Fatalf("refused: %v", it)
	}
	if e := estimate(t, it[1]); !near(e["usd"].(float64), 0.03) || e["list_usd"] != 0.04 {
		t.Errorf("25%% off $0.04 = %v", e)
	}

	// The allowance used up: the quote says so as the job would, and /api/me
	// counts it.
	for i := range free.ImagesPerWindow {
		if status, m, _ := in.post(alice, fmt.Sprintf("q%d", i), body(freeOffer, "free")); status != http.StatusAccepted {
			t.Fatalf("run %d: %d %v", i, status, m)
		}
	}
	ref, _ := items(t, quote())[0]["refusal"].(map[string]any)
	if ref["error"] != "free_limit" || ref["frees_at"] == nil {
		t.Errorf("refusal = %v", ref)
	}
	me := jsonOf(t, in.do(alice, http.MethodGet, "/api/me", nil))
	images := me["free"].(map[string]any)["images"].(map[string]any)
	if images["used"] != 5.0 || images["left"] != 0.0 || images["frees_at"] == nil {
		t.Errorf("images = %v", images)
	}

	// A run on the person's key at the discounted price, with that price shown.
	status, m, _ := in.post(alice, "q-paid", body(paidOffer, "key", `"shown_usd":0.03`))
	if status != http.StatusAccepted {
		t.Fatalf("%d %v", status, m)
	}
	var kept string
	w.pool.QueryRow(context.Background(), `SELECT (estimate->>'discount') || ' ' || (estimate->>'list_usd') FROM job WHERE id=$1`, idOf(t, m)).Scan(&kept)
	if kept != "0.25 0.04" {
		t.Errorf("the job keeps %q of its discount", kept)
	}
}

// A press with several frames is its jobs in the database: one batch, each
// job with the frames it makes, read back by GET and by a repeat.
func TestAPressIsItsJobsInTheDatabase(t *testing.T) {
	w := newWorld(t)
	in := w.instance()
	key := "press-1"
	body := `{"prompt":"a fox","frames":[{"offer":"` + freeOffer + `","pay":"key"},{"offer":"` + freeOffer + `","pay":"key"},{"offer":"` + paidOffer + `","pay":"key"}]}`
	post := func() (int, map[string]any, http.Header) {
		resp := in.do(alice, http.MethodPost, "/api/batches", strings.NewReader(body), "Content-Type", "application/json", "Idempotency-Key", key)
		return resp.StatusCode, jsonOf(t, resp), resp.Header
	}
	code, m, _ := post()
	if code != http.StatusAccepted {
		t.Fatalf("%d %v", code, m)
	}
	list := m["jobs"].([]any)
	if len(list) != 2 {
		t.Fatalf("%d jobs: %v", len(list), m)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["batch"] != m["batch"] || fmt.Sprint(first["frames"]) != "[0 1]" || first["settings"].(map[string]any)["n"] != 2.0 {
		t.Errorf("first %v %v %v", first["batch"], first["frames"], first["settings"])
	}
	if second["batch"] != m["batch"] || fmt.Sprint(second["frames"]) != "[2]" {
		t.Errorf("second %v %v", second["batch"], second["frames"])
	}
	id := idOf(t, first)
	got := jsonOf(t, in.do(alice, http.MethodGet, fmt.Sprintf("/api/jobs/%d", id), nil))
	if got["batch"] != m["batch"] || fmt.Sprint(got["frames"]) != "[0 1]" {
		t.Errorf("GET %v %v", got["batch"], got["frames"])
	}
	code, again, hdr := post()
	if code != http.StatusAccepted || hdr.Get("Idempotent-Replayed") != "true" || again["batch"] != m["batch"] || len(again["jobs"].([]any)) != 2 {
		t.Errorf("repeat %d %q %v", code, hdr.Get("Idempotent-Replayed"), again)
	}
	var n int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM framy.job WHERE batch IS NOT NULL`).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d jobs in the batch, %v", n, err)
	}
	// Bob has none of it.
	if resp := in.do(bob, http.MethodGet, fmt.Sprintf("/api/jobs/%d", id), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob: %d", resp.StatusCode)
	}
}
