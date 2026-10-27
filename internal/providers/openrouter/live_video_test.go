// SPDX-License-Identifier: Apache-2.0
//go:build live

// The live video tests run on OpenRouter itself and spend real credit. They
// were written and compiled but not run when the adapter was made.
//
// Each paid test makes one video on x-ai/grok-imagine-video-1.5-lite at 480p
// for 1 second, which the catalog prices at 2 cents a second: about $0.02. Two
// tests are paid, so about $0.04 in all; a key with a low limit caps it. The
// test that polls a job that does not exist is free.
//
//	OPENROUTER_TEST_KEY_FILE=/path/to/key go test -tags=live -run LiveVideo -v ./internal/providers/openrouter/
//
// The key goes nowhere but the Authorization header. OPENROUTER_TEST_OUT, when
// set, is a directory the video is written to.
//
// TestLiveVideoCallback checks what no fake can: that OpenRouter takes the
// callback_url, that a delivery arrives, and how it is signed. It needs a
// receiver the internet reaches. The test runs framy's own Hook on
// OPENROUTER_TEST_CALLBACK_LISTEN (127.0.0.1:8099 by default); point a tunnel
// at it and give the tunnel's https origin:
//
//	cloudflared tunnel --url http://127.0.0.1:8099
//	OPENROUTER_TEST_CALLBACK_ORIGIN=https://….trycloudflare.com \
//	OPENROUTER_TEST_WEBHOOK_SECRET_FILE=/path/to/signing-secret \
//	OPENROUTER_TEST_KEY_FILE=/path/to/key go test -tags=live -run LiveVideoCallback -v ./internal/providers/openrouter/
//
// OPENROUTER_TEST_WEBHOOK_SECRET_FILE is optional: it is the signing secret of
// the workspace the key belongs to, and with it the test also checks the
// signature. To learn whether a callback_url works with a key a person gets
// through the OAuth flow, put that key in OPENROUTER_TEST_KEY_FILE (with no
// webhook secret file: a person's workspace secret is not framy's to have).
package openrouter

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

const liveVideoModel = "x-ai/grok-imagine-video-1.5-lite"

// liveVideoOffer is the cheapest run of the model, by hand: the catalog sync
// would read the same from /videos/models, but needs 60 more calls to do it.
func liveVideoOffer() catalog.Offer {
	return catalog.Offer{
		ID: catalog.OfferID(catalog.OpenRouter, liveVideoModel), ModelID: liveVideoModel, Provider: catalog.OpenRouter,
		ProviderModelID: liveVideoModel, Kind: catalog.Video,
		Params: map[string]catalog.Param{
			"resolution": {Type: catalog.Enum, Values: []any{"480p"}},
			"duration":   {Type: catalog.Enum, Values: []any{1}},
		},
	}
}

func liveVideoJob(id int64) jobs.Job {
	of := liveVideoOffer()
	return jobs.Job{
		ID: id, UserID: 1, OfferID: of.ID, ModelID: of.ModelID, Provider: of.Provider, Kind: catalog.Video,
		Prompt: "A red apple on a wooden table, soft daylight, slow push in", Free: true, Offer: of,
		Settings: map[string]any{"resolution": "480p", "duration": 1},
	}
}

func liveVideoProvider(key string, cb Callbacks) *Provider {
	return New(Config{
		UserAgent: "framy-live-test (+https://github.com/FreshLabDev/framy)",
		Referer:   "https://github.com/FreshLabDev/framy",
		Title:     "framy",
		Callbacks: cb,
		Key:       func(context.Context, jobs.Job) (string, error) { return key, nil },
	})
}

// awaitVideo asks every few seconds until the job is over, as the queue does
// every 30.
func awaitVideo(t *testing.T, p *Provider, j jobs.Job, until time.Duration) jobs.Outcome {
	t.Helper()
	deadline := time.Now().Add(until)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		out, err := p.Poll(ctx, j)
		cancel()
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if !out.Running {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("the video was not done after %s", until)
		}
		time.Sleep(5 * time.Second)
	}
}

func checkVideo(t *testing.T, out jobs.Outcome) {
	t.Helper()
	if out.Err != nil {
		t.Fatalf("the video failed: %v", out.Err)
	}
	if len(out.Results) != 1 || len(out.Results[0].Data) == 0 || !strings.HasPrefix(out.Results[0].ContentType, "video/") {
		t.Fatalf("results: %d", len(out.Results))
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 || *out.CostUSD > 0.10 {
		t.Errorf("cost %v: the price must come back, and a 1 s video at 480p is about $0.02", out.CostUSD)
	}
	t.Logf("video: %s, %d bytes, cost %s", out.Results[0].ContentType, len(out.Results[0].Data), cost(out))
	if dir := os.Getenv("OPENROUTER_TEST_OUT"); dir != "" {
		path := filepath.Join(dir, "video.mp4")
		if err := os.WriteFile(path, out.Results[0].Data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
	}
}

// A video from submit to download by polling alone, with no callback: what
// framy does without HOOK_PATH_SECRET. About $0.02.
func TestLiveVideoByPolling(t *testing.T) {
	p := liveVideoProvider(liveKey(t), Callbacks{})
	j := liveVideoJob(1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sub, err := p.Submit(ctx, j)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if sub.ProviderJobID == "" {
		t.Fatal("OpenRouter returned no job id")
	}
	t.Logf("job %s accepted", sub.ProviderJobID)
	j.ProviderJobID = sub.ProviderJobID
	if sub.Outcome != nil && !sub.Outcome.Running {
		checkVideo(t, *sub.Outcome)
		return
	}
	checkVideo(t, awaitVideo(t, p, j, 10*time.Minute))
}

// What Poll says about an id OpenRouter does not have. It costs nothing, and
// shows the status the queue gets: an error with no class, asked again until
// the run's hour is up.
func TestLiveVideoPollOfAJobThatDoesNotExist(t *testing.T) {
	p := liveVideoProvider(liveKey(t), Callbacks{})
	j := liveVideoJob(1)
	j.ProviderJobID = "gen-vid-1700000000-doesnotexist000000000"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := p.Poll(ctx, j)
	if err == nil {
		t.Fatalf("no error for a job that does not exist: %+v", out)
	}
	var pe *jobs.Error
	t.Logf("poll of a missing job: %v (classified: %v)", err, errors.As(err, &pe))
}

// liveReceiver is Deliveries in memory, and a window on what arrived.
type liveReceiver struct {
	mu      sync.Mutex
	seen    []liveDelivery
	arrived chan struct{}
}

// liveDelivery is the shape of a webhook, without anything secret in it.
type liveDelivery struct {
	status        int
	signed        bool
	signatureForm bool // t=<digits>,v1=<hex>
	idempotency   bool
}

func (l *liveReceiver) Job(context.Context, int64) (HookJob, error) {
	return HookJob{Free: true, Waiting: true}, nil
}

func (l *liveReceiver) Record(context.Context, int64, []string) (bool, bool, error) {
	return true, true, nil
}

// shape wraps the Hook to note what each delivery looked like and how the Hook
// answered it.
func (l *liveReceiver) shape(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig := r.Header.Get(signatureHeader)
		d := liveDelivery{
			signed:      sig != "",
			idempotency: r.Header.Get(idempotencyHeader) != "",
		}
		if t, rest, ok := strings.Cut(sig, ","); ok {
			d.signatureForm = strings.HasPrefix(t, "t=") && strings.HasPrefix(rest, "v1=")
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		d.status = rec.status
		l.mu.Lock()
		l.seen = append(l.seen, d)
		l.mu.Unlock()
		select {
		case l.arrived <- struct{}{}:
		default:
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// About $0.02. Submits one video with a callback_url on the tunnel, and waits
// for the delivery: whether OpenRouter takes the URL for this key, whether a
// webhook comes, and whether its signature checks out against the secret.
func TestLiveVideoCallback(t *testing.T) {
	key := liveKey(t)
	origin := os.Getenv("OPENROUTER_TEST_CALLBACK_ORIGIN")
	if origin == "" {
		t.Skip("set OPENROUTER_TEST_CALLBACK_ORIGIN to the https origin of a tunnel to OPENROUTER_TEST_CALLBACK_LISTEN")
	}
	var secret []byte
	if path := os.Getenv("OPENROUTER_TEST_WEBHOOK_SECRET_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the webhook secret file: %v", err)
		}
		secret = []byte(strings.TrimSpace(string(raw)))
	}

	pathSecret := make([]byte, 32)
	if _, err := rand.Read(pathSecret); err != nil {
		t.Fatal(err)
	}
	cb := Callbacks{PublicURL: origin, Secret: pathSecret}
	if !cb.Enabled() {
		t.Fatalf("OPENROUTER_TEST_CALLBACK_ORIGIN must be an https origin")
	}
	recv := &liveReceiver{arrived: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.Handle(HookPattern, recv.shape(&Hook{Callbacks: cb, Deliveries: recv, SigningSecret: secret}))
	ln, err := net.Listen("tcp", env("OPENROUTER_TEST_CALLBACK_LISTEN", "127.0.0.1:8099"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })

	p := liveVideoProvider(key, cb)
	j := liveVideoJob(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sub, err := p.Submit(ctx, j)
	if err != nil {
		t.Fatalf("OpenRouter did not take the request with a callback_url: %v", err)
	}
	t.Logf("job %s accepted with a callback_url; waiting for the webhook on %s", sub.ProviderJobID, ln.Addr())
	j.ProviderJobID = sub.ProviderJobID

	select {
	case <-recv.arrived:
	case <-time.After(10 * time.Minute):
		t.Error("no webhook arrived in 10 minutes: the key's kind may not deliver, or the tunnel is down")
	}
	recv.mu.Lock()
	for i, d := range recv.seen {
		t.Logf("delivery %d: answered %d, signature header %v (form t=…,v1=…: %v), idempotency key %v", i+1, d.status, d.signed, d.signatureForm, d.idempotency)
		switch {
		case d.status == http.StatusNotFound:
			t.Errorf("delivery %d: the path did not pass the MAC check", i+1)
		case d.status == http.StatusUnauthorized:
			t.Errorf("delivery %d: the signature did not match the secret: the format or the secret is not what the adapter assumes", i+1)
		case len(secret) > 0 && d.status == http.StatusNoContent && !d.signed:
			t.Errorf("delivery %d: no signature header", i+1)
		}
	}
	recv.mu.Unlock()
	if len(secret) == 0 {
		t.Log("no webhook secret was given, so the signature was not checked")
	}

	// And the video itself, to leave nothing running.
	checkVideo(t, awaitVideo(t, p, j, 10*time.Minute))
}
