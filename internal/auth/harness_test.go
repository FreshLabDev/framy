// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/web"
)

// testClock moves only when told to. It starts at the real time, since
// PostgreSQL and the JWT library have their own ideas of now.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now().Truncate(time.Second)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// memSessions is Sessions in memory, with a count of the lookups and a hook
// to stop one half way.
type memSessions struct {
	mu      sync.Mutex
	rows    map[[32]byte]Session
	lookups int
	err     error
	// inLookup, when set, is called after a lookup read its row and before it
	// answers.
	inLookup func()
}

func newMemSessions() *memSessions { return &memSessions{rows: map[[32]byte]Session{}} }

func (m *memSessions) Create(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.rows[s.Hash] = s
	return nil
}

func (m *memSessions) Lookup(_ context.Context, hash [32]byte, kind Kind, now time.Time) (Session, bool, error) {
	m.mu.Lock()
	m.lookups++
	s, ok := m.rows[hash]
	err, hook := m.err, m.inLookup
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return Session{}, false, err
	}
	if !ok || s.Kind != kind || !s.Expires.After(now) {
		return Session{}, false, nil
	}
	return s, true, nil
}

func (m *memSessions) Delete(_ context.Context, hash [32]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	delete(m.rows, hash)
	return nil
}

func (m *memSessions) DeleteUser(_ context.Context, userID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	var n int64
	for h, s := range m.rows {
		if s.UserID == userID {
			delete(m.rows, h)
			n++
		}
	}
	return n, nil
}

func (m *memSessions) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for h, s := range m.rows {
		if s.Expires.Before(before) {
			delete(m.rows, h)
			n++
		}
	}
	return n, nil
}

func (m *memSessions) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *memSessions) lookupCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lookups
}

// memPeople records who was touched.
type memPeople struct {
	mu      sync.Mutex
	touched []tg.User
	err     error
}

func (p *memPeople) Touch(_ context.Context, u tg.User, chat *tg.Chat) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if chat != nil {
		panic("sign-in touches a person, never a chat")
	}
	if p.err != nil {
		return p.err
	}
	p.touched = append(p.touched, u)
	return nil
}

func (p *memPeople) users() []tg.User {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]tg.User(nil), p.touched...)
}

// harness is the whole thing on one page: the public server with the sign-in
// routes mounted as main.go mounts them, a fake Telegram, and a browser.
type harness struct {
	t        *testing.T
	clock    *testClock
	idp      *fakeIDP
	auth     *Auth
	srv      *web.Server
	sessions Sessions
	people   People
	logs     *bytes.Buffer
}

type harnessOpts struct {
	sessions Sessions
	people   People
	noClient bool
	limits   web.Limits
	// withLimits keeps the real rate limits; otherwise the auth class is
	// unlimited, since a test makes many sign-ins from one address.
	withLimits bool
}

func newHarness(t *testing.T, opts ...func(*harnessOpts)) *harness {
	t.Helper()
	o := harnessOpts{}
	for _, f := range opts {
		f(&o)
	}
	if o.sessions == nil {
		o.sessions = newMemSessions()
	}
	if o.people == nil {
		o.people = &memPeople{}
	}
	clock := newTestClock()
	h := &harness{t: t, clock: clock, sessions: o.sessions, people: o.people, logs: &bytes.Buffer{}}
	h.idp = newFakeIDP(t, clock)
	cfg := Config{
		BotToken: testBotToken,
		Issuer:   h.idp.URL,
		Sessions: o.sessions,
		People:   o.people,
		Log:      slog.New(slog.NewTextHandler(&syncWriter{w: h.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:      clock.Now,
	}
	if !o.noClient {
		cfg.ClientID, cfg.ClientSecret, cfg.SiteURL = testClientID, testClientSecret, testSiteURL
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.auth = a
	wcfg := web.Config{Person: a.PersonKey, R2Endpoint: "https://abc123.r2.cloudflarestorage.com"}
	if !o.withLimits {
		wcfg.Limits = web.Limits{web.ClassAuth: web.ClassLimit{}}
	}
	if o.limits != nil {
		wcfg.Limits = o.limits
	}
	srv, err := web.New(wcfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Mount(srv)
	// What other code does with the contract.
	srv.Handle("GET /api/me", a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Person(r)
		if !ok {
			t.Error("Person said nobody inside Require")
		}
		writeJSON(w, http.StatusOK, map[string]int64{"id": id})
	})))
	srv.Handle("GET /api/open", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Person(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": ok})
	}))
	h.srv = srv
	return h
}

func withSessions(s Sessions) func(*harnessOpts) { return func(o *harnessOpts) { o.sessions = s } }
func withPeople(p People) func(*harnessOpts)     { return func(o *harnessOpts) { o.people = p } }
func withoutClient() func(*harnessOpts)          { return func(o *harnessOpts) { o.noClient = true } }
func withRealLimits() func(*harnessOpts)         { return func(o *harnessOpts) { o.withLimits = true } }

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (h *harness) logText() string { return h.logs.String() }

// resp is one answer, read in full.
type resp struct {
	code   int
	header http.Header
	body   string
}

func (r resp) location() string { return r.header.Get("Location") }

// cookie returns the Set-Cookie of the name, or nil.
func (r resp) cookie(name string) *http.Cookie {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// rawSetCookie is the Set-Cookie line of the name, as sent.
func (r resp) rawSetCookie(name string) string {
	for _, line := range r.header.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	return ""
}

// browser keeps cookies and talks to the site directly and to the provider
// over HTTP. Redirects are followed by hand so a test sees every step.
type browser struct {
	h       *harness
	cookies map[string]string
	bearer  string
}

func (h *harness) browser() *browser { return &browser{h: h, cookies: map[string]string{}} }

// do sends a request to the site or to the provider. url may be absolute, or a
// path on the site. A POST says it comes from the site itself, as a browser's
// fetch does.
func (b *browser) do(method, target string, body string, headers ...string) resp {
	b.h.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = testSiteURL + target
	}
	if strings.HasPrefix(target, b.h.idp.URL) {
		req, err := http.NewRequest(method, target, strings.NewReader(body))
		if err != nil {
			b.h.t.Fatal(err)
		}
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := c.Do(req)
		if err != nil {
			b.h.t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return resp{code: res.StatusCode, header: res.Header, body: string(raw)}
	}
	if !strings.HasPrefix(target, testSiteURL) {
		b.h.t.Fatalf("the browser was sent to %q, which is neither the site nor the provider", target)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Content-Type", "application/json")
	}
	for name, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: v})
	}
	if b.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+b.bearer)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	b.h.srv.ServeHTTP(rec, req)
	res := rec.Result()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	return resp{code: res.StatusCode, header: res.Header, body: rec.Body.String()}
}

func (b *browser) get(target string) resp { return b.do(http.MethodGet, target, "") }

// startLogin opens /auth/login and returns the Telegram URL it sent the
// browser to.
func (b *browser) startLogin(returnTo string) string {
	b.h.t.Helper()
	target := "/auth/login"
	if returnTo != "" {
		target += "?return_to=" + url.QueryEscape(returnTo)
	}
	r := b.get(target)
	if r.code != http.StatusSeeOther || !strings.HasPrefix(r.location(), b.h.idp.URL+"/auth?") {
		b.h.t.Fatalf("login: status %d, Location %q, body %q", r.code, r.location(), r.body)
	}
	return r.location()
}

// approve is the person pressing Confirm at Telegram: it follows the
// authorization URL and returns the callback URL it redirects to.
func (b *browser) approve(authURL string) string {
	b.h.t.Helper()
	r := b.get(authURL)
	if r.code != http.StatusFound {
		b.h.t.Fatalf("authorize: status %d, body %q", r.code, r.body)
	}
	return r.location()
}

// signIn does the whole round trip and returns the callback's answer.
func (b *browser) signIn(returnTo string) resp {
	b.h.t.Helper()
	return b.get(b.approve(b.startLogin(returnTo)))
}

// me asks /api/me, which sits behind Require.
func (b *browser) me() resp { return b.get("/api/me") }

// signedIn reports who the site thinks this browser is.
func (b *browser) signedIn() (int64, bool) {
	b.h.t.Helper()
	r := b.get("/api/open")
	var out struct {
		ID int64 `json:"id"`
		OK bool  `json:"ok"`
	}
	mustJSON(b.h.t, r.body, &out)
	return out.ID, out.OK
}
