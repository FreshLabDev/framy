// SPDX-License-Identifier: Apache-2.0
//go:build integration

// Video through the real queue and the real hook, against a fake OpenRouter
// built from the documented shapes (video_fake_test.go):
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/providers/openrouter/
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// personKey is the key of a person who runs on their own account.
const personKey = "sk-or-v1-the-persons-own-key"

// syncBuffer is a log destination that several goroutines write.
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

// stack is the queue, the adapter and the hook, wired as in cmd/framy.
type stack struct {
	t       *testing.T
	pool    *pgxpool.Pool
	f       *fakeOR
	q       *jobs.Queue
	sink    *sink
	logs    *syncBuffer
	cb      Callbacks
	hookURL string
}

type rigOptions struct {
	pollEvery time.Duration
	// noCallbacks leaves the callbacks off, as without HOOK_PATH_SECRET.
	noCallbacks bool
	// maxVideo bounds a finished video; the adapter's default when zero.
	maxVideo int64
}

func newStack(t *testing.T, o rigOptions) *stack {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	of := videoOffer()
	params, _ := json.Marshal(of.Params)
	routes, _ := json.Marshal(of.Routes)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`TRUNCATE framy.webhook_delivery`, nil},
		{`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ($1, 'video', 'Test video', 'test') ON CONFLICT DO NOTHING`, []any{of.ModelID}},
		{`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, params, routes, fetched_at)
		  VALUES ($1, $2, 'openrouter', $3, 'Test video', $4, $5, now())
		  ON CONFLICT (id) DO UPDATE SET params = EXCLUDED.params, routes = EXCLUDED.routes`,
			[]any{of.ID, of.ModelID, of.ProviderModelID, string(params), string(routes)}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}

	r := &stack{t: t, pool: pool, f: newFakeOR(t), sink: &sink{kept: map[int64][]jobs.Result{}}, logs: &syncBuffer{}, cb: testCallbacks()}
	if o.noCallbacks {
		r.cb = Callbacks{}
	}
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapter := New(Config{
		Base:      r.f.base(),
		HTTP:      r.f.srv.Client(),
		Title:     "framy",
		Callbacks: r.cb,
		MaxVideo:  o.maxVideo,
		// A free run is paid with framy's key, any other with the person's.
		Key: func(_ context.Context, j jobs.Job) (string, error) {
			if j.Free {
				return fakeKey, nil
			}
			return personKey, nil
		},
	})
	q, err := jobs.NewQueue(pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openrouter": adapter},
		Sink:      r.sink,
		Log:       log,
		WaitSlot:  100 * time.Millisecond,
		PollEvery: o.pollEvery,
		FetchPoll: 50 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.q = q
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(stop)
	})

	mux := http.NewServeMux()
	mux.Handle(HookPattern, &Hook{
		Callbacks:     r.cb,
		Deliveries:    &HookStore{Pool: pool, Wake: q.PollNowTx},
		SigningSecret: signingSecret,
		Log:           log,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.hookURL = srv.URL

	// Whatever the test did, no key and no callback path reached a log.
	t.Cleanup(func() {
		logs := r.logs.String()
		for _, secret := range []string{fakeKey, personKey, string(signingSecret), string(r.cb.Secret), "hooks/or/", "X-Amz-Signature"} {
			if secret != "" && strings.Contains(logs, secret) {
				t.Errorf("the log has %q in it:\n%s", secret, logs)
			}
		}
	})
	return r
}

// enqueue starts a video job.
func (r *stack) enqueue(free bool, settings map[string]any) int64 {
	r.t.Helper()
	id, created, err := r.q.Enqueue(context.Background(), jobs.New{
		UserID: person, Surface: jobs.DM, ChatID: person, Offer: videoOffer(),
		Prompt: "a fox in the snow", Settings: settings, Free: free,
	})
	if err != nil || !created {
		r.t.Fatalf("enqueue: %d %v %v", id, created, err)
	}
	return id
}

func (r *stack) job(id int64) jobs.Job {
	r.t.Helper()
	j, err := r.q.Get(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return j
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// riverState is where River has the job's queue entry: scheduled while it
// sleeps between polls.
func (r *stack) riverState(id int64) string {
	r.t.Helper()
	var state string
	err := r.pool.QueryRow(context.Background(), `
		SELECT r.state::text FROM framy.river_job r JOIN framy.job j ON j.river_job_id = r.id WHERE j.id = $1`, id).Scan(&state)
	if err != nil {
		r.t.Fatal(err)
	}
	return state
}

// asleep waits for a job to be submitted, saved and put to sleep until its
// next poll: the state a webhook finds a real video in for most of its life.
func (r *stack) asleep(id int64) {
	r.t.Helper()
	waitFor(r.t, "the job to sleep between polls", func() bool {
		return r.job(id).ProviderJobID == r.f.id && r.riverState(id) == "scheduled"
	})
}

func (r *stack) deliveries() int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM framy.webhook_delivery`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *stack) polls() int {
	_, polls, _ := r.f.counts()
	return polls
}

// post sends a webhook to the hook's server: the path of job id's callback
// URL, and the headers as given.
func (r *stack) post(path, body string, header map[string]string) int {
	r.t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.hookURL+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// callbackPath is the path part of the callback_url the job was sent with.
func (r *stack) callbackPath(id int64) string {
	u, err := url.Parse(r.cb.URL(id))
	if err != nil {
		r.t.Fatal(err)
	}
	return u.Path
}

// signedHeaders are the headers of a delivery signed by OpenRouter's
// workspace of framy's own key.
func signedHeaders(secret []byte, at time.Time, body, idem string) map[string]string {
	h := map[string]string{"X-OpenRouter-Signature": sign(secret, at, body)}
	if idem != "" {
		h["X-OpenRouter-Idempotency-Key"] = idem
	}
	return h
}

func (r *stack) settled(id int64) jobs.Job {
	r.t.Helper()
	var j jobs.Job
	waitFor(r.t, fmt.Sprintf("job %d to end", id), func() bool {
		j = r.job(id)
		return j.Status.Final()
	})
	return j
}

func (r *stack) kept(id int64) []jobs.Result {
	r.sink.mu.Lock()
	defer r.sink.mu.Unlock()
	return append([]jobs.Result(nil), r.sink.kept[id]...)
}

var longPoll = rigOptions{pollEvery: 10 * time.Minute}

// The webhook is the only thing that can finish this job inside the test: the
// poll is ten minutes away.
func TestAVideoFinishesWhenTheWebhookArrives(t *testing.T) {
	r := newStack(t, longPoll)
	id := r.enqueue(true, map[string]any{"resolution": "768p", "duration": 5})
	r.asleep(id)

	sent := r.f.lastSubmit()
	if sent["callback_url"] != r.cb.URL(id) {
		t.Fatalf("the request carried callback_url %v", sent["callback_url"])
	}
	if j := r.job(id); j.Status != jobs.Running || j.Submits != 1 {
		t.Fatalf("before the webhook: %s, %d submits", j.Status, j.Submits)
	}
	if r.polls() != 0 {
		t.Fatalf("%d polls before the webhook", r.polls())
	}

	r.f.complete(0.036)
	body := fmt.Sprintf(`{"id":%q,"status":"completed"}`, r.f.id)
	if code := r.post(r.callbackPath(id), body, signedHeaders(signingSecret, time.Now(), body, "evt-1")); code != http.StatusNoContent {
		t.Fatalf("the webhook was answered %d", code)
	}

	j := r.settled(id)
	if j.Status != jobs.Done || j.Submits != 1 || j.ActualUSD == nil || *j.ActualUSD != 0.036 {
		t.Fatalf("after the webhook: %s %s, %d submits, actual %v", j.Status, j.Error, j.Submits, j.ActualUSD)
	}
	kept := r.kept(id)
	if len(kept) != 1 || kept[0].ContentType != "video/mp4" || !bytes.Equal(kept[0].Data, mp4) {
		t.Fatalf("results kept: %+v", kept)
	}
	submits, polls, downloads := r.f.counts()
	if submits != 1 || polls != 1 || downloads != 1 {
		t.Errorf("%d submits, %d polls, %d downloads: one of each", submits, polls, downloads)
	}
	// The key that paid is the key that fetched, and nothing else carried it.
	submitAuth, pollAuth, downloadAuth := r.f.authorizations()
	for what, auths := range map[string][]string{"poll": pollAuth, "download": downloadAuth, "submit": submitAuth} {
		for _, a := range auths {
			if a != "Bearer "+fakeKey {
				t.Errorf("the %s carried %q", what, a)
			}
		}
	}
	if r.deliveries() != 2 {
		t.Errorf("%d deliveries remembered, want the idempotency key and the signature", r.deliveries())
	}
}

// Without HOOK_PATH_SECRET there is no callback and no hook; the 30 s poll (here
// 150 ms) does everything.
func TestAVideoFinishesByPollingAlone(t *testing.T) {
	r := newStack(t, rigOptions{pollEvery: 150 * time.Millisecond, noCallbacks: true})
	id := r.enqueue(true, map[string]any{"duration": 5})

	waitFor(t, "two polls that find it running", func() bool { return r.polls() >= 2 })
	if j := r.job(id); j.Status != jobs.Running {
		t.Fatalf("%s while OpenRouter was still working", j.Status)
	}
	r.f.complete(0.02)
	j := r.settled(id)
	if j.Status != jobs.Done || j.ActualUSD == nil || *j.ActualUSD != 0.02 || j.Submits != 1 {
		t.Fatalf("%s %s, actual %v, %d submits", j.Status, j.Error, j.ActualUSD, j.Submits)
	}
	if _, ok := r.f.lastSubmit()["callback_url"]; ok {
		t.Error("a callback_url was sent with callbacks off")
	}
	if submits, _, downloads := r.f.counts(); submits != 1 || downloads != 1 {
		t.Errorf("%d submits and %d downloads", submits, downloads)
	}
	if len(r.kept(id)) != 1 {
		t.Errorf("%d results kept", len(r.kept(id)))
	}
}

func TestAForgedExpiredOrReplayedWebhookWakesNothing(t *testing.T) {
	r := newStack(t, longPoll)
	id := r.enqueue(true, nil)
	r.asleep(id)
	path := r.callbackPath(id)
	body := fmt.Sprintf(`{"id":%q,"status":"in_progress"}`, r.f.id)
	asleepStill := func(what string) {
		t.Helper()
		// A woken job is fetched within 50 ms and polled at once.
		time.Sleep(600 * time.Millisecond)
		if r.polls() != 0 || r.riverState(id) != "scheduled" {
			t.Fatalf("%s: %d polls, River state %s", what, r.polls(), r.riverState(id))
		}
		if r.deliveries() != 0 {
			t.Fatalf("%s: %d deliveries remembered", what, r.deliveries())
		}
	}

	for name, tc := range map[string]struct {
		path   string
		header map[string]string
		want   int
	}{
		"a signature of another secret":      {path, signedHeaders([]byte("another-secret"), time.Now(), body, "f1"), http.StatusUnauthorized},
		"a signature from ten minutes ago":   {path, signedHeaders(signingSecret, time.Now().Add(-10*time.Minute), body, "f2"), http.StatusUnauthorized},
		"a signature from ten minutes ahead": {path, signedHeaders(signingSecret, time.Now().Add(10*time.Minute), body, "f3"), http.StatusUnauthorized},
		"no signature":                       {path, map[string]string{"X-OpenRouter-Idempotency-Key": "f4"}, http.StatusUnauthorized},
		"a body that was changed": {path, func() map[string]string {
			return signedHeaders(signingSecret, time.Now(), `{"status":"failed"}`, "f5")
		}(), http.StatusUnauthorized},
		"a path with the wrong mac": {"/hooks/or/" + fmt.Sprint(id) + "/" + strings.Repeat("A", 43), signedHeaders(signingSecret, time.Now(), body, "f6"), http.StatusNotFound},
		"the mac of another job":    {"/hooks/or/" + fmt.Sprint(id) + "/" + r.cb.MAC(id+1), signedHeaders(signingSecret, time.Now(), body, "f7"), http.StatusNotFound},
	} {
		if code := r.post(tc.path, body, tc.header); code != tc.want {
			t.Errorf("%s: answered %d, want %d", name, code, tc.want)
		}
	}
	asleepStill("after every forgery")

	// One good delivery brings one poll forward; OpenRouter says it is still
	// running, so the job sleeps again.
	good := signedHeaders(signingSecret, time.Now(), body, "evt-1")
	if code := r.post(path, body, good); code != http.StatusNoContent {
		t.Fatalf("the good webhook was answered %d", code)
	}
	waitFor(t, "the poll the webhook brought forward", func() bool { return r.polls() == 1 && r.riverState(id) == "scheduled" })
	if r.deliveries() != 2 {
		t.Fatalf("%d deliveries remembered", r.deliveries())
	}

	// The same delivery again, or OpenRouter's retry of it with a new
	// timestamp, or a replay under another idempotency key, does nothing.
	retry := signedHeaders(signingSecret, time.Now().Add(2*time.Second), body, "evt-1")
	other := map[string]string{"X-OpenRouter-Signature": good["X-OpenRouter-Signature"], "X-OpenRouter-Idempotency-Key": "evt-replay"}
	for name, h := range map[string]map[string]string{"the same request": good, "a retry": retry, "the same signature, another key": other} {
		if code := r.post(path, body, h); code != http.StatusNoContent {
			t.Errorf("%s was answered %d: the sender must stop", name, code)
		}
	}
	time.Sleep(600 * time.Millisecond)
	if r.polls() != 1 {
		t.Fatalf("%d polls after the replays, want the one", r.polls())
	}

	// A new event is a new poll.
	next := `{"id":"` + r.f.id + `","status":"in_progress","progress":50}`
	if code := r.post(path, next, signedHeaders(signingSecret, time.Now(), next, "evt-2")); code != http.StatusNoContent {
		t.Fatalf("the next webhook was answered %d", code)
	}
	waitFor(t, "the second poll", func() bool { return r.polls() == 2 && r.riverState(id) == "scheduled" })
	if j := r.job(id); j.Status != jobs.Running || j.Submits != 1 || len(r.kept(id)) != 0 {
		t.Errorf("%s, %d submits, %d kept", j.Status, j.Submits, len(r.kept(id)))
	}
}

// On a person's own key the webhook is only a signal: what it says is not
// believed, and the run is re-read with the person's key.
func TestAPersonsWebhookOnlyTriggersAReread(t *testing.T) {
	r := newStack(t, longPoll)
	r.f.key = personKey
	id := r.enqueue(false, nil)
	r.asleep(id)
	path := r.callbackPath(id)

	// A webhook that claims the run is done, and carries no signature at all.
	lie := fmt.Sprintf(`{"id":%q,"status":"completed","usage":{"cost":0}}`, r.f.id)
	if code := r.post(path, lie, map[string]string{"X-OpenRouter-Idempotency-Key": "p-1"}); code != http.StatusNoContent {
		t.Fatalf("answered %d", code)
	}
	waitFor(t, "the re-read", func() bool { return r.polls() == 1 && r.riverState(id) == "scheduled" })
	if j := r.job(id); j.Status != jobs.Running || len(r.kept(id)) != 0 || j.ActualUSD != nil {
		t.Fatalf("the webhook's word was taken: %s, %d kept, actual %v", j.Status, len(r.kept(id)), j.ActualUSD)
	}
	if _, pollAuth, _ := r.f.authorizations(); len(pollAuth) != 1 || pollAuth[0] != "Bearer "+personKey {
		t.Errorf("the re-read used %q", pollAuth)
	}

	r.f.complete(0.07)
	truth := fmt.Sprintf(`{"id":%q,"status":"completed"}`, r.f.id)
	if code := r.post(path, truth, map[string]string{"X-OpenRouter-Idempotency-Key": "p-2"}); code != http.StatusNoContent {
		t.Fatalf("answered %d", code)
	}
	j := r.settled(id)
	if j.Status != jobs.Done || j.ActualUSD == nil || *j.ActualUSD != 0.07 || len(r.kept(id)) != 1 {
		t.Fatalf("%s %s, actual %v, %d kept", j.Status, j.Error, j.ActualUSD, len(r.kept(id)))
	}
	// Every call of the run was on the person's key; framy's own was never
	// used for them.
	submitAuth, pollAuth, downloadAuth := r.f.authorizations()
	every := append(append(submitAuth, pollAuth...), downloadAuth...)
	for _, a := range every {
		if a != "Bearer "+personKey {
			t.Errorf("a call carried %q", a)
		}
	}
	// And their idempotency keys are all that was remembered of the webhooks.
	if r.deliveries() != 2 {
		t.Errorf("%d deliveries remembered", r.deliveries())
	}
}

// The paid request returned and the id could not be saved: the worker
// "crashed" between the two. The retry must not send the request again.
func TestACrashBetweenSubmitAndSaveDoesNotPayTwice(t *testing.T) {
	r := newStack(t, rigOptions{pollEvery: 150 * time.Millisecond})
	ctx := context.Background()
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS test_block_provider_id ON framy.job`,
		`CREATE OR REPLACE FUNCTION framy.test_block_provider_id() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN
		   IF NEW.provider_job_id IS NOT NULL AND NEW.provider_job_id IS DISTINCT FROM OLD.provider_job_id THEN
		     RAISE EXCEPTION 'test: the provider job id cannot be saved';
		   END IF;
		   RETURN NEW;
		 END $$`,
		`CREATE TRIGGER test_block_provider_id BEFORE UPDATE OF provider_job_id ON framy.job
		 FOR EACH ROW EXECUTE FUNCTION framy.test_block_provider_id()`,
	} {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = r.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS test_block_provider_id ON framy.job`)
		_, _ = r.pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS framy.test_block_provider_id()`)
	})
	r.f.complete(0.036)

	id := r.enqueue(true, nil)
	// River tries again after a second or so.
	j := r.settled(id)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Interrupted || j.ProviderJobID != "" || j.ActualUSD != nil || j.Submits != 1 {
		t.Fatalf("after the retry: %s %s, provider id %q, actual %v, %d submits", j.Status, j.ErrorClass, j.ProviderJobID, j.ActualUSD, j.Submits)
	}
	if submits, _, _ := r.f.counts(); submits != 1 {
		t.Errorf("%d paid requests: the retry paid again", submits)
	}
	// The log has the id, so a person can look the run up at OpenRouter.
	if logs := r.logs.String(); !strings.Contains(logs, "job provider id not saved") || !strings.Contains(logs, r.f.id) {
		t.Errorf("the id of the paid run is not in the log:\n%s", logs)
	}
}

// What the queue makes of each way a video can end or be turned away.
func TestVideoFailuresThroughTheQueue(t *testing.T) {
	cost := 0.0
	for _, tc := range []struct {
		name  string
		setup func(r *stack)
		class jobs.Class
		// submits is how many paid requests the run took in all.
		submits int
		// polled: the job got as far as a provider id.
		polled bool
		// paid: OpenRouter's usage was recorded.
		paid bool
		in   string
	}{
		{
			name: "a request OpenRouter turns away",
			setup: func(r *stack) {
				r.f.submitReply = reply400(`{"error":{"code":400,"message":"resolution is not supported"}}`)
			},
			class: jobs.Refused, submits: 1, in: "resolution is not supported",
		},
		{
			name: "a prompt the filter stops at the door",
			setup: func(r *stack) {
				r.f.submitReply = reply400(`{"error":{"code":400,"message":"blocked","metadata":{"error_type":"content_policy_violation","reasons":["violence"]}}}`)
			},
			class: jobs.Moderation, submits: 1, in: "violence",
		},
		{
			name: "a video the filter stops after the run began",
			setup: func(r *stack) {
				r.f.cost = &cost
				r.f.set(videoFailed, `"The video was blocked by the content policy"`)
			},
			class: jobs.Moderation, submits: 1, polled: true, paid: true, in: "content policy",
		},
		{
			name: "a provider that failed",
			setup: func(r *stack) {
				r.f.cost = &cost
				r.f.set(videoFailed, `{"code":500,"message":"model crashed","metadata":{"provider_name":"Alibaba"}}`)
			},
			class: jobs.ProviderFailed, submits: 1, polled: true, paid: true, in: "model crashed",
		},
		{
			name:  "a job OpenRouter cancelled",
			setup: func(r *stack) { r.f.set(videoCancelled, "") },
			class: jobs.ProviderFailed, submits: 1, polled: true, in: "cancelled",
		},
		{
			name:  "a job that expired at OpenRouter",
			setup: func(r *stack) { r.f.set(videoExpired, "") },
			class: jobs.Timeout, submits: 1, polled: true, in: "expired",
		},
		{
			name: "a video too large to keep",
			setup: func(r *stack) {
				r.f.complete(0.036)
				r.f.video = bytes.Repeat([]byte("v"), 1<<20+1)
			},
			class: jobs.Storage, submits: 1, polled: true, paid: true, in: "larger than",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newStack(t, rigOptions{pollEvery: 100 * time.Millisecond, noCallbacks: true, maxVideo: 1 << 20})
			tc.setup(r)
			id := r.enqueue(true, nil)
			j := r.settled(id)
			if j.Status != jobs.Failed || j.ErrorClass != tc.class || j.Submits != tc.submits {
				t.Fatalf("%s %s (%s), %d submits", j.Status, j.ErrorClass, j.Error, j.Submits)
			}
			if !strings.Contains(j.Error, tc.in) {
				t.Errorf("the reason %q lacks %q", j.Error, tc.in)
			}
			if got := j.ProviderJobID != ""; got != tc.polled {
				t.Errorf("provider job id saved: %v, want %v", got, tc.polled)
			}
			if got := j.ActualUSD != nil; got != tc.paid {
				t.Errorf("cost recorded: %v, want %v (%v)", got, tc.paid, j.ActualUSD)
			}
			if n := len(r.kept(id)); n != 0 {
				t.Errorf("%d results kept for a run that failed", n)
			}
			if submits, _, _ := r.f.counts(); submits != tc.submits {
				t.Errorf("%d requests reached OpenRouter", submits)
			}
		})
	}
}

func reply400(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}
}

// A refusal before the run began is the one failure that is sent again: it
// was not paid for. The run that goes through is paid once.
func TestARateLimitedRequestIsSentAgainAndPaidOnce(t *testing.T) {
	r := newStack(t, rigOptions{pollEvery: 100 * time.Millisecond, noCallbacks: true})
	var mu sync.Mutex
	calls := 0
	r.f.complete(0.036)
	r.f.submitReply = func(w http.ResponseWriter) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"slow down"}}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":%q,"status":"pending"}`, r.f.id)
	}
	id := r.enqueue(true, nil)
	j := r.settled(id)
	if j.Status != jobs.Done || j.Submits != 2 || j.ActualUSD == nil || *j.ActualUSD != 0.036 {
		t.Fatalf("%s %s, %d submits, actual %v", j.Status, j.Error, j.Submits, j.ActualUSD)
	}
	if submits, _, downloads := r.f.counts(); submits != 2 || downloads != 1 {
		t.Errorf("%d requests and %d downloads", submits, downloads)
	}
}

func TestTheHooksTableRemembersADeliveryOnceAndForgetsOldOnes(t *testing.T) {
	r := newStack(t, longPoll)
	ctx := context.Background()
	id := r.enqueue(true, nil)
	r.asleep(id)
	store := &HookStore{Pool: r.pool, Wake: r.q.PollNowTx}

	// An old row, from a week ago and more, goes with the next delivery.
	if _, err := r.pool.Exec(ctx, `INSERT INTO framy.webhook_delivery (provider, job_id, key, received_at) VALUES ('openrouter', $1, 'idem:old', now() - interval '8 days')`, id); err != nil {
		t.Fatal(err)
	}
	fresh, woken, err := store.Record(ctx, id, []string{"idem:a", "sig:b"})
	if err != nil || !fresh || !woken {
		t.Fatalf("first: fresh %v, woken %v, %v", fresh, woken, err)
	}
	if n := r.deliveries(); n != 2 {
		t.Errorf("%d rows: the old one stays or the new ones are missing", n)
	}
	// Seeing either key again is a duplicate, and wakes nothing.
	for _, keys := range [][]string{{"idem:a", "sig:b"}, {"idem:a", "sig:other"}, {"idem:other", "sig:b"}, {"idem:a"}} {
		fresh, woken, err := store.Record(ctx, id, keys)
		if err != nil || fresh || woken {
			t.Errorf("%v: fresh %v, woken %v, %v", keys, fresh, woken, err)
		}
	}
	// A delivery with no keys at all is always new, and wakes.
	waitFor(t, "the poll of the first delivery", func() bool { return r.polls() == 1 && r.riverState(id) == "scheduled" })
	if fresh, woken, err := store.Record(ctx, id, nil); err != nil || !fresh || !woken {
		t.Errorf("no keys: fresh %v, woken %v, %v", fresh, woken, err)
	}
	// The delivery and the wake are one step: if the wake fails, nothing is
	// remembered, so OpenRouter's retry can try again.
	before := r.deliveries()
	broken := &HookStore{Pool: r.pool, Wake: func(context.Context, pgx.Tx, int64) (bool, error) { return false, errors.New("wake failed") }}
	if _, _, err := broken.Record(ctx, id, []string{"idem:z", "sig:z"}); err == nil {
		t.Error("a failed wake was not reported")
	}
	if n := r.deliveries(); n != before {
		t.Errorf("%d rows remembered for a delivery that failed, want %d", n, before)
	}
}

func TestTheHooksStoreKnowsWhoPaysAndWhoWaits(t *testing.T) {
	r := newStack(t, longPoll)
	ctx := context.Background()
	store := &HookStore{Pool: r.pool, Wake: r.q.PollNowTx}

	if _, err := store.Job(ctx, 987654); err != ErrNoJob {
		t.Errorf("a job that does not exist: %v", err)
	}
	freeID := r.enqueue(true, nil)
	r.asleep(freeID)
	if j, err := store.Job(ctx, freeID); err != nil || !j.Free || !j.Waiting {
		t.Errorf("a free run in the background: %+v, %v", j, err)
	}
	// A run that ended is not waiting.
	if _, err := r.pool.Exec(ctx, `UPDATE framy.job SET status = 'failed', finished_at = now() WHERE id = $1`, freeID); err != nil {
		t.Fatal(err)
	}
	if j, err := store.Job(ctx, freeID); err != nil || j.Waiting {
		t.Errorf("an ended run: %+v, %v", j, err)
	}
	// A run whose id is not saved yet is not waiting either.
	if _, err := r.pool.Exec(ctx, `UPDATE framy.job SET status = 'running', finished_at = NULL, provider_job_id = NULL WHERE id = $1`, freeID); err != nil {
		t.Fatal(err)
	}
	if j, err := store.Job(ctx, freeID); err != nil || j.Waiting {
		t.Errorf("a run with no provider id: %+v, %v", j, err)
	}
}
