// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/web"
)

const (
	siteOrigin = "https://framy.test"
	ann        = int64(5001)
	bob        = int64(5002)
)

// clock moves only when told to.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_800_000_000, 0).UTC()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
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

// memStore is a Store in memory with the behaviour the interface promises,
// so that the logic of the handlers is tested without a database. The same
// contract test (testStoreContract) runs against it and against PGStore.
type memStore struct {
	mu    sync.Mutex
	flows map[[sha256.Size]byte]Flow
	// failPut makes Put fail, for the path where a flow cannot be kept.
	failPut bool
}

func newMemStore() *memStore { return &memStore{flows: map[[sha256.Size]byte]Flow{}} }

func (m *memStore) Put(_ context.Context, f Flow, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return fmt.Errorf("store is down")
	}
	for h, old := range m.flows {
		if !old.ExpiresAt.After(now) || (old.UserID == f.UserID && old.Provider == f.Provider) {
			delete(m.flows, h)
		}
	}
	m.flows[f.State] = f
	return nil
}

func (m *memStore) Take(_ context.Context, state [sha256.Size]byte) (Flow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.flows[state]
	if !ok {
		return Flow{}, ErrNoFlow
	}
	delete(m.flows, state)
	return f, nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.flows)
}

// peek is the flow of a state, without spending it, for a test to learn the
// verifier.
func (m *memStore) peek(t *testing.T, state string) Flow {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.flows[hashState(state)]
	if !ok {
		t.Fatal("no flow for the state")
	}
	return f
}

// fakeKeys is the Saver: it records what the callback saves.
type fakeKeys struct {
	mu    sync.Mutex
	saves []save
	err   error
}

type save struct {
	user     int64
	provider string
	key      string
}

func (f *fakeKeys) Save(_ context.Context, user int64, provider, key string) (keys.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return keys.Connection{}, f.err
	}
	f.saves = append(f.saves, save{user, provider, key})
	return keys.Connection{Provider: provider, UserID: user, Last4: key[len(key)-4:], Status: keys.Valid}, nil
}

func (f *fakeKeys) saved() []save {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]save(nil), f.saves...)
}

// fakeOpenRouter stands in for /auth, POST /api/v1/auth/keys and GET
// /api/v1/key. It is stricter than the real one in the one way the tests
// care about: the exchange succeeds only if the verifier hashes (S256, by
// code of its own here, not framy's) to the challenge the code was issued
// for, and a code works once. Its refusals quote the code and the verifier
// back, as a provider's error may, so a leak of either shows.
type fakeOpenRouter struct {
	*httptest.Server
	mu      sync.Mutex
	pending map[string]string // code -> challenge
	minted  map[string]bool
	last    string
	// Counts of requests that reached each endpoint.
	auths, exchanges, checks int
	// force, when set, is the status the exchange answers with; body is its
	// content (a default error when empty); delay holds the answer back.
	force int
	body  string
	delay time.Duration
	// redirect, with force set to a 3xx, is where the exchange sends the
	// caller: another host, as a hijacked endpoint could.
	redirect string
	// rejectKey makes /key refuse every key, as a revoked one would be.
	rejectKey bool
	// authQuery is the query of the last /auth request.
	authQuery url.Values
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newFakeOpenRouter(t *testing.T) *fakeOpenRouter {
	t.Helper()
	f := &fakeOpenRouter{pending: map[string]string{}, minted: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.auths++
		f.authQuery = q
		f.mu.Unlock()
		if q.Get("callback_url") != siteOrigin+CallbackPath || q.Get("code_challenge_method") != "S256" ||
			len(q.Get("code_challenge")) != 43 || q.Get("state") == "" {
			http.Error(w, "bad authorization request", http.StatusBadRequest)
			return
		}
		code := "or-code-" + randHex(12)
		f.mu.Lock()
		f.pending[code] = q.Get("code_challenge")
		f.mu.Unlock()
		// What OpenRouter does once the person presses Authorize.
		target := q.Get("callback_url") + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("POST /api/v1/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Code     string `json:"code"`
			Verifier string `json:"code_verifier"`
			Method   string `json:"code_challenge_method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.exchanges++
		force, body, delay := f.force, f.body, f.delay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		refuse := func(status int) {
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"message":"Invalid code %s (verifier %s)"}}`, in.Code, in.Verifier)
		}
		if force != 0 {
			if force >= 300 && force < 400 {
				f.mu.Lock()
				target := f.redirect
				f.mu.Unlock()
				http.Redirect(w, r, target, force)
				return
			}
			if body == "" {
				refuse(force)
				return
			}
			w.WriteHeader(force)
			_, _ = w.Write([]byte(body))
			return
		}
		f.mu.Lock()
		challenge, ok := f.pending[in.Code]
		if ok {
			delete(f.pending, in.Code)
		}
		f.mu.Unlock()
		if !ok || in.Method != "S256" || challenge != s256(in.Verifier) {
			refuse(http.StatusBadRequest)
			return
		}
		key := "sk-or-v1-minted" + randHex(24)
		f.mu.Lock()
		f.minted[key] = true
		f.last = key
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"key": key, "user_id": "or-user"})
	})
	// The check of a key that keys.Service makes before it stores one.
	mux.HandleFunc("GET /api/v1/key", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.checks++
		ok := f.minted[key] && !f.rejectKey
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error":{"message":"No auth credentials found for %s"}}`, key)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"label":"framy","usage":0}}`))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOpenRouter) lastKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (f *fakeOpenRouter) counts() (auths, exchanges, checks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auths, f.exchanges, f.checks
}

func (f *fakeOpenRouter) set(fn func(f *fakeOpenRouter)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// harness is the site with the OAuth routes on the real web server, in front
// of a stand-in for OpenRouter.
type harness struct {
	t     *testing.T
	conn  *Connector
	web   *web.Server
	or    *fakeOpenRouter
	store *memStore
	keys  *fakeKeys
	clock *clock
	logs  *syncBuffer

	mu         sync.Mutex
	transcript strings.Builder
}

// who is signed in is named by a header of the test's own: sign-in is
// another package's.
const whoHeader = "X-Test-User"

func person(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.Header.Get(whoHeader), 10, 64)
	return id, err == nil && id != 0
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, or: newFakeOpenRouter(t), store: newMemStore(), keys: &fakeKeys{},
		clock: newClock(), logs: &syncBuffer{},
	}
	h.conn = h.newConnector(Config{})
	return h
}

// newConnector builds a Connector for the harness with cfg's non-zero fields
// over the harness's own, and puts it on a fresh web server.
func (h *harness) newConnector(over Config) *Connector {
	h.t.Helper()
	cfg := Config{
		Store: h.store, Keys: h.keys, Person: person, SiteURL: siteOrigin,
		AuthURL: h.or.URL + "/auth", APIBase: h.or.URL + "/api/v1",
		UserAgent: "framy-test", Now: h.clock.Now,
		Log: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if over.HTTP != nil {
		cfg.HTTP = over.HTTP
	}
	if over.Keys != nil {
		cfg.Keys = over.Keys
	}
	if over.Store != nil {
		cfg.Store = over.Store
	}
	conn, err := New(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	// The auth class is left unlimited: a test makes more requests from one
	// address than 20 a minute.
	srv, err := web.New(web.Config{Limits: web.Limits{web.ClassAuth: {}}})
	if err != nil {
		h.t.Fatal(err)
	}
	srv.HandleFunc("GET "+StartPath, conn.Start)
	srv.HandleFunc("GET "+CallbackPath, conn.Callback, web.Deadlines(10*time.Second, 45*time.Second))
	h.web = srv
	h.conn = conn
	return conn
}

// browser is a person's browser: a jar of the cookies the site set, and the
// person signed in on it (0 for nobody).
type browser struct {
	h    *harness
	user int64
	jar  map[string]string
}

func (h *harness) browser(user int64) *browser {
	return &browser{h: h, user: user, jar: map[string]string{}}
}

// get sends GET target (a path and query of the site) with the browser's
// cookies, records the answer in the transcript and takes in its cookies.
func (b *browser) get(target string) *httptest.ResponseRecorder {
	b.h.t.Helper()
	return b.request(http.MethodGet, target)
}

func (b *browser) request(method, target string) *httptest.ResponseRecorder {
	b.h.t.Helper()
	r := httptest.NewRequest(method, siteOrigin+target, nil)
	if b.user != 0 {
		r.Header.Set(whoHeader, strconv.FormatInt(b.user, 10))
	}
	names := make([]string, 0, len(b.jar))
	for name := range b.jar {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r.AddCookie(&http.Cookie{Name: name, Value: b.jar[name]})
	}
	rr := httptest.NewRecorder()
	b.h.web.ServeHTTP(rr, r)
	b.h.record(rr)
	for _, c := range rr.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.jar, c.Name)
		} else {
			b.jar[c.Name] = c.Value
		}
	}
	return rr
}

// record keeps everything the site answered, status, headers and body, to
// look for secrets in.
func (h *harness) record(rr *httptest.ResponseRecorder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	res := rr.Result()
	fmt.Fprintf(&h.transcript, "HTTP %d\n", res.StatusCode)
	keys := make([]string, 0, len(res.Header))
	for k := range res.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range res.Header[k] {
			fmt.Fprintf(&h.transcript, "%s: %s\n", k, v)
		}
	}
	h.transcript.WriteString(rr.Body.String())
	h.transcript.WriteString("\n---\n")
}

func (h *harness) said() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.transcript.String()
}

// start opens the flow and returns OpenRouter's URL it redirects to.
func (b *browser) start() string {
	b.h.t.Helper()
	rr := b.get(StartPath)
	if rr.Code != http.StatusSeeOther {
		b.h.t.Fatalf("start answered %d: %s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, b.h.or.URL+"/auth?") {
		b.h.t.Fatalf("start redirects to %q", loc)
	}
	return loc
}

// approve is the person pressing Authorize at OpenRouter: it returns the
// callback target (path and query) OpenRouter sends the browser to.
func (b *browser) approve(authURL string) string {
	b.h.t.Helper()
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Get(authURL)
	if err != nil {
		b.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusTemporaryRedirect || err != nil {
		b.h.t.Fatalf("OpenRouter answered %d with %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if loc.Scheme+"://"+loc.Host != siteOrigin || loc.Path != CallbackPath {
		b.h.t.Fatalf("OpenRouter calls back %q", loc)
	}
	return loc.RequestURI()
}

// callback opens the callback target and returns how the flow ended.
func (b *browser) callback(target string) Result {
	b.h.t.Helper()
	return b.h.resultOf(b.get(target))
}

// connect is a whole flow in one browser.
func (b *browser) connect() (Result, string) {
	b.h.t.Helper()
	target := b.approve(b.start())
	return b.callback(target), target
}

// resultOf reads where a callback sent the browser.
func (h *harness) resultOf(rr *httptest.ResponseRecorder) Result {
	h.t.Helper()
	if rr.Code != http.StatusSeeOther {
		h.t.Fatalf("the callback answered %d, want a redirect: %s", rr.Code, rr.Body.String())
	}
	loc, err := url.Parse(rr.Header().Get("Location"))
	if err != nil || loc.IsAbs() || loc.Path != "/" || loc.Query().Get("connect") != "openrouter" || len(loc.Query()) != 2 {
		h.t.Fatalf("the callback redirects to %q, want /?connect=openrouter&result=…", rr.Header().Get("Location"))
	}
	return Result(loc.Query().Get("result"))
}

// callbackState is the state and the code in a callback target.
func callbackParams(t *testing.T, target string) (state, code string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state"), u.Query().Get("code")
}

// assertNoSecrets fails if any of the secrets, or a long piece of one,
// appears in the logs or in anything the site answered.
func (h *harness) assertNoSecrets(secrets ...string) {
	h.t.Helper()
	places := map[string]string{"the log": h.logs.String(), "the responses": h.said()}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		forms := []string{s, s[:min(len(s), 12)], url.QueryEscape(s), base64.StdEncoding.EncodeToString([]byte(s))}
		for where, text := range places {
			for _, f := range forms {
				if strings.Contains(text, f) {
					h.t.Errorf("%s holds %q", where, f)
				}
			}
		}
	}
}
