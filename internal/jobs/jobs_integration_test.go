// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The queue needs a real PostgreSQL:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/jobs/
package jobs

import (
	"context"
	"errors"
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
	"github.com/riverqueue/river/rivertype"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/migrations"
)

const (
	alice = int64(1001)
	bob   = int64(1002)
)

var (
	imageOffer = catalog.Offer{ID: "openrouter:test/image", ModelID: "test/image", Provider: "openrouter", ProviderModelID: "test/image", Kind: catalog.Image}
	videoOffer = catalog.Offer{ID: "openrouter:test/video", ModelID: "test/video", Provider: "openrouter", ProviderModelID: "test/video", Kind: catalog.Video}
)

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
	if err := store.Migrate(ctx, migrations.FS, MigrateRiver); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range []string{
		`TRUNCATE framy.job, framy.river_job`,
		`INSERT INTO core.person (telegram_user_id) VALUES (1001), (1002) ON CONFLICT DO NOTHING`,
		`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ('test/image', 'image', 'Test image', 'test'), ('test/video', 'video', 'Test video', 'test') ON CONFLICT DO NOTHING`,
		`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, fetched_at) VALUES
		   ('openrouter:test/image', 'test/image', 'openrouter', 'test/image', 'Test image', now()),
		   ('openrouter:test/video', 'test/video', 'openrouter', 'test/video', 'Test video', now()) ON CONFLICT DO NOTHING`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return pool
}

func testQueue(t *testing.T, p *fakeProvider) (*Queue, *pgxpool.Pool) {
	t.Helper()
	pool := testPool(t)
	q, err := NewQueue(pool, Config{
		Providers: map[string]Provider{"openrouter": p},
		Sink:      p,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		PollEvery: 100 * time.Millisecond,
		StopGrace: 5 * time.Second,
		FetchPoll: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return q, pool
}

// start runs River's client for the rest of the test.
func start(t *testing.T, q *Queue) {
	t.Helper()
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(ctx)
	})
}

// fakeProvider is an upstream that counts what it was asked and keeps the
// results it is given back.
type fakeProvider struct {
	mu       sync.Mutex
	submit   func(context.Context, Job) (Submission, error)
	poll     func(context.Context, Job) (Outcome, error)
	submits  map[int64]int
	polls    map[int64]int
	inFlight map[int64]int
	most     map[int64]int
	kept     map[int64]int
}

func newFake() *fakeProvider {
	return &fakeProvider{
		submits: map[int64]int{}, polls: map[int64]int{}, inFlight: map[int64]int{}, most: map[int64]int{}, kept: map[int64]int{},
		submit: func(context.Context, Job) (Submission, error) { return done(0.02), nil },
	}
}

func done(cost float64) Submission {
	return Submission{Outcome: &Outcome{Results: []Result{{ContentType: "image/png", Data: []byte("png")}}, CostUSD: &cost}}
}

func (f *fakeProvider) Submit(ctx context.Context, j Job) (Submission, error) {
	f.mu.Lock()
	f.submits[j.ID]++
	f.inFlight[j.UserID]++
	f.most[j.UserID] = max(f.most[j.UserID], f.inFlight[j.UserID])
	run := f.submit
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight[j.UserID]--
		f.mu.Unlock()
	}()
	return run(ctx, j)
}

func (f *fakeProvider) Poll(ctx context.Context, j Job) (Outcome, error) {
	f.mu.Lock()
	f.polls[j.ID]++
	run := f.poll
	f.mu.Unlock()
	return run(ctx, j)
}

func (f *fakeProvider) Keep(_ context.Context, j Job, results []Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kept[j.ID] += len(results)
	return nil
}

func (f *fakeProvider) count(m map[int64]int, k int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[k]
}

func job(user int64, of catalog.Offer) New {
	return New{UserID: user, Surface: DM, ChatID: user, Offer: of, Prompt: "a fox in the snow"}
}

func enqueue(t *testing.T, q *Queue, n New) int64 {
	t.Helper()
	id, created, err := q.Enqueue(context.Background(), n)
	if err != nil || !created {
		t.Fatalf("enqueue: %d %v %v", id, created, err)
	}
	return id
}

func get(t *testing.T, q *Queue, id int64) Job {
	t.Helper()
	j, err := q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func isSnooze(err error) bool {
	var s *rivertype.JobSnoozeError
	return errors.As(err, &s)
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestADuplicateUpdateStartsNothing(t *testing.T) {
	q, pool := testQueue(t, newFake())
	ctx := context.Background()
	n := job(alice, imageOffer)
	n.DedupeKey = TelegramUpdate(42)
	first, created, err := q.Enqueue(ctx, n)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	again, created, err := q.Enqueue(ctx, n)
	if err != nil || created || again != first {
		t.Fatalf("again = %d, created %v, %v; want job %d back", again, created, err, first)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job WHERE kind='framy_generate'`); n != 1 {
		t.Fatalf("river jobs = %d, want 1", n)
	}

	// "Generate" pressed twice, or delivered twice, queues the draft once.
	d := job(alice, imageOffer)
	d.DedupeKey = TelegramUpdate(43)
	draft, _, err := q.Draft(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Generate(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if err := q.Generate(ctx, draft); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("second Generate = %v, want ErrNotDraft", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.river_job WHERE kind='framy_generate'`); n != 2 {
		t.Fatalf("river jobs = %d, want 2", n)
	}
}

func TestTheThirdJobOfAPersonWaits(t *testing.T) {
	p := newFake()
	release := make(chan struct{})
	p.submit = func(ctx context.Context, _ Job) (Submission, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return Submission{}, ctx.Err()
		}
		return done(0.02), nil
	}
	q, _ := testQueue(t, p)
	a1 := enqueue(t, q, job(alice, imageOffer))
	a2 := enqueue(t, q, job(alice, imageOffer))
	a3 := enqueue(t, q, job(alice, imageOffer))
	b1 := enqueue(t, q, job(bob, imageOffer))
	start(t, q)

	eventually(t, "two of alice's jobs and bob's running", func() bool {
		return p.count(p.inFlight, alice) == 2 && p.count(p.inFlight, bob) == 1
	})
	// A few rounds of looking for a slot change nothing.
	time.Sleep(500 * time.Millisecond)
	if j := get(t, q, a3); j.Status != Queued || p.count(p.submits, a3) != 0 {
		t.Fatalf("the third job: %s, %d requests", j.Status, p.count(p.submits, a3))
	}
	close(release)
	for _, id := range []int64{a1, a2, a3, b1} {
		eventually(t, "every job done", func() bool { return get(t, q, id).Status == Done })
	}
	if most := p.count(p.most, alice); most != 2 {
		t.Fatalf("alice ran %d jobs at once, want 2", most)
	}
}

func TestAKilledWorkerDoesNotPayTwice(t *testing.T) {
	ctx := context.Background()
	t.Run("an answer that never came", func(t *testing.T) {
		p := newFake()
		hang := make(chan struct{})
		p.submit = func(context.Context, Job) (Submission, error) {
			// The process dies here: the call never returns while the
			// test looks.
			<-hang
			return done(0.02), nil
		}
		q, _ := testQueue(t, p)
		id := enqueue(t, q, job(alice, imageOffer))
		dead := make(chan struct{})
		go func() { defer close(dead); _ = q.work(ctx, id) }()
		eventually(t, "the paid request out", func() bool { return p.count(p.submits, id) == 1 })

		// River rescues the job and works it again.
		if err := q.work(ctx, id); err != nil {
			t.Fatal(err)
		}
		j := get(t, q, id)
		if p.count(p.submits, id) != 1 || j.Status != Failed || j.ErrorClass != Interrupted || j.Submits != 1 {
			t.Fatalf("after the rescue: %d requests, %s %s, submits %d", p.count(p.submits, id), j.Status, j.ErrorClass, j.Submits)
		}
		close(hang)
		<-dead
		if j := get(t, q, id); j.Status != Failed {
			t.Fatalf("the late answer changed the ending to %s", j.Status)
		}
	})

	t.Run("a run in the background", func(t *testing.T) {
		p := newFake()
		p.submit = func(context.Context, Job) (Submission, error) { return Submission{ProviderJobID: "gen-1"}, nil }
		polls := 0
		p.poll = func(context.Context, Job) (Outcome, error) {
			polls++
			if polls == 1 {
				return Outcome{Running: true}, nil
			}
			cost := 1.40
			return Outcome{Results: []Result{{ContentType: "video/mp4", URL: "https://example.test/v.mp4"}}, CostUSD: &cost}, nil
		}
		q, _ := testQueue(t, p)
		id := enqueue(t, q, job(alice, videoOffer))
		if err := q.work(ctx, id); !isSnooze(err) {
			t.Fatalf("after the submit: %v, want a snooze until the first poll", err)
		}
		if j := get(t, q, id); j.ProviderJobID != "gen-1" {
			t.Fatalf("provider job id = %q, want it saved right after the submit", j.ProviderJobID)
		}
		// Each later attempt, a rescue included, asks instead of paying.
		if err := q.work(ctx, id); !isSnooze(err) {
			t.Fatalf("first poll: %v", err)
		}
		if err := q.work(ctx, id); err != nil {
			t.Fatal(err)
		}
		j := get(t, q, id)
		if p.count(p.submits, id) != 1 || p.count(p.polls, id) != 2 || j.Status != Done || p.count(p.kept, id) != 1 {
			t.Fatalf("%d requests, %d polls, %s, %d kept", p.count(p.submits, id), p.count(p.polls, id), j.Status, p.count(p.kept, id))
		}
	})
}

func TestAFinishedJobHasItsEstimateAndItsCost(t *testing.T) {
	p := newFake()
	p.submit = func(context.Context, Job) (Submission, error) { return done(0.0201), nil }
	q, pool := testQueue(t, p)
	n := job(alice, imageOffer)
	n.Estimate = &price.Estimate{
		Offer: imageOffer.ID, USD: 0.0195, MaxUSD: 0.0195, Kind: price.About,
		Lines: []price.Line{{Billable: "output_image", Unit: "token", Quantity: 1024, RateUSD: 0.000019, USD: 0.019456}},
	}
	id := enqueue(t, q, n)
	if err := q.work(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	j := get(t, q, id)
	if j.Status != Done || j.EstimateUSD == nil || *j.EstimateUSD != 0.0195 || j.ActualUSD == nil || *j.ActualUSD != 0.0201 {
		t.Fatalf("job: %s, estimate %v, actual %v", j.Status, j.EstimateUSD, j.ActualUSD)
	}
	if kind := scalar[string](t, pool, `SELECT estimate->>'kind' || ' ' || (estimate->'lines'->0->>'billable') FROM framy.job WHERE id=$1`, id); kind != "about output_image" {
		t.Fatalf("estimate kept as %q", kind)
	}
	if results := scalar[int](t, pool, `SELECT results FROM framy.job WHERE id=$1`, id); results != 1 {
		t.Fatalf("results = %d", results)
	}
}

func TestOnlyARefusalBeforeTheRunIsRetried(t *testing.T) {
	ctx := context.Background()
	t.Run("429, then the run", func(t *testing.T) {
		p := newFake()
		calls := 0
		p.submit = func(context.Context, Job) (Submission, error) {
			calls++
			if calls == 1 {
				return Submission{}, &Error{Class: RateLimited, RetryAfter: time.Minute}
			}
			return done(0.02), nil
		}
		q, pool := testQueue(t, p)
		id := enqueue(t, q, job(alice, imageOffer))
		if err := q.work(ctx, id); !isSnooze(err) {
			t.Fatalf("after a 429: %v, want a snooze", err)
		}
		j := get(t, q, id)
		if j.Status != Queued || j.SubmittedAt != nil || j.RetryAt == nil || time.Until(*j.RetryAt) < 50*time.Second {
			t.Fatalf("after a 429: %s, submitted %v, retry at %v", j.Status, j.SubmittedAt, j.RetryAt)
		}
		// Not before the provider's own wait.
		if err := q.work(ctx, id); !isSnooze(err) || p.count(p.submits, id) != 1 {
			t.Fatalf("early attempt: %v, %d requests", err, p.count(p.submits, id))
		}
		if _, err := pool.Exec(ctx, `UPDATE framy.job SET retry_at=now() WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if err := q.work(ctx, id); err != nil {
			t.Fatal(err)
		}
		if j := get(t, q, id); j.Status != Done || j.Submits != 2 {
			t.Fatalf("after the retry: %s, %d submits", j.Status, j.Submits)
		}
	})

	for _, tc := range []struct {
		name  string
		err   error
		class Class
	}{
		{"no credit", &Error{Class: Money}, Money},
		{"moderation", &Error{Class: Moderation}, Moderation},
		{"no answer in time", context.DeadlineExceeded, Timeout},
		{"an error the adapter did not classify", errors.New("connection reset"), ProviderFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFake()
			p.submit = func(context.Context, Job) (Submission, error) { return Submission{}, tc.err }
			q, _ := testQueue(t, p)
			id := enqueue(t, q, job(alice, imageOffer))
			if err := q.work(ctx, id); err != nil {
				t.Fatal(err)
			}
			if err := q.work(ctx, id); err != nil {
				t.Fatal(err)
			}
			if j := get(t, q, id); j.Status != Failed || j.ErrorClass != tc.class || p.count(p.submits, id) != 1 {
				t.Fatalf("%s %s, %d requests; want failed %s after one", j.Status, j.ErrorClass, p.count(p.submits, id), tc.class)
			}
		})
	}

	t.Run("a provider that keeps refusing", func(t *testing.T) {
		p := newFake()
		p.submit = func(context.Context, Job) (Submission, error) { return Submission{}, &Error{Class: Unavailable} }
		q, pool := testQueue(t, p)
		id := enqueue(t, q, job(alice, imageOffer))
		for range maxSubmits {
			_ = q.work(ctx, id)
			if _, err := pool.Exec(ctx, `UPDATE framy.job SET retry_at=now() WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		if j := get(t, q, id); j.Status != Failed || j.ErrorClass != Unavailable || j.Submits != maxSubmits {
			t.Fatalf("%s %s after %d submits", j.Status, j.ErrorClass, j.Submits)
		}
	})
}

func TestNothingIsPaidWithoutAnAdapter(t *testing.T) {
	q, pool := testQueue(t, newFake())
	q.cfg.Providers = nil
	id := enqueue(t, q, job(alice, imageOffer))
	if err := q.work(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if j := get(t, q, id); j.Status != Failed || j.ErrorClass != NoAdapter || j.Submits != 0 {
		t.Fatalf("%s %s, %d submits", j.Status, j.ErrorClass, j.Submits)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM framy.job WHERE status='running'`); n != 0 {
		t.Fatalf("%d jobs still hold a slot", n)
	}
}

func TestCancel(t *testing.T) {
	q, pool := testQueue(t, newFake())
	ctx := context.Background()
	id := enqueue(t, q, job(alice, imageOffer))
	if err := q.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j := get(t, q, id); j.Status != Cancelled {
		t.Fatalf("status %s", j.Status)
	}
	if state := scalar[string](t, pool, `SELECT r.state::text FROM framy.river_job r JOIN framy.job j ON j.river_job_id=r.id WHERE j.id=$1`, id); state != "cancelled" {
		t.Fatalf("river job %s", state)
	}
	// A cancelled job is not worked.
	if err := q.work(ctx, id); err != nil || get(t, q, id).Status != Cancelled {
		t.Fatalf("work after cancel: %v", err)
	}

	running := enqueue(t, q, job(bob, imageOffer))
	if _, err := pool.Exec(ctx, `UPDATE framy.job SET status='running', started_at=now() WHERE id=$1`, running); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(ctx, running); !errors.Is(err, ErrRunning) {
		t.Fatalf("cancel a running job: %v", err)
	}
}

func TestTheSweep(t *testing.T) {
	q, pool := testQueue(t, newFake())
	ctx := context.Background()
	stale, _, err := q.Draft(ctx, job(alice, imageOffer))
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := q.Draft(ctx, job(alice, imageOffer))
	if err != nil {
		t.Fatal(err)
	}
	given := enqueue(t, q, job(bob, imageOffer))
	if _, err := pool.Exec(ctx, `UPDATE framy.job SET created_at = now() - interval '11 minutes' WHERE id = $1`, stale); err != nil {
		t.Fatal(err)
	}
	// A job rescued on its last attempt: River discards it unworked.
	if _, err := pool.Exec(ctx, `UPDATE framy.river_job SET state='discarded', finalized_at=now()
		WHERE id = (SELECT river_job_id FROM framy.job WHERE id = $1)`, given); err != nil {
		t.Fatal(err)
	}
	if err := q.Generate(ctx, stale); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("Generate on a stale draft: %v", err)
	}
	if n, err := q.sweep(ctx); err != nil || n != 2 {
		t.Fatalf("sweep = %d, %v; want 2", n, err)
	}
	for id, want := range map[int64]Status{stale: Cancelled, fresh: Draft, given: Failed} {
		if j := get(t, q, id); j.Status != want {
			t.Errorf("job %d: %s, want %s", id, j.Status, want)
		}
	}
}

func TestAPersonsJobsStartInOrder(t *testing.T) {
	q, pool := testQueue(t, newFake())
	ctx := context.Background()
	var ids []int64
	for range 4 {
		ids = append(ids, enqueue(t, q, job(alice, imageOffer)))
	}
	// The first two run; then the first ends and frees a slot.
	if _, err := pool.Exec(ctx, `UPDATE framy.job SET status='running', started_at=now() WHERE id = ANY($1)`, ids[:2]); err != nil {
		t.Fatal(err)
	}
	if err := q.end(ctx, ids[0], ending{status: Done, results: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.work(ctx, ids[3]); !isSnooze(err) {
		t.Fatalf("the fourth job went ahead of the third: %v", err)
	}
	if err := q.work(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	if j := get(t, q, ids[2]); j.Status != Done {
		t.Fatalf("the third job: %s", j.Status)
	}
}
