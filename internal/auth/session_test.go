// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// signInAs signs a new browser in as the person, through the provider.
func (h *harness) signInAs(id int64) *browser {
	h.t.Helper()
	h.idp.userID = id
	b := h.browser()
	if cb := b.signIn(""); cb.location() != "/" {
		h.t.Fatalf("sign-in as %d: %d -> %q", id, cb.code, cb.location())
	}
	return b
}

func TestRequire(t *testing.T) {
	h := newHarness(t)
	signed := h.signInAs(testUserID)
	app := h.browser()
	h.mini(app, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusOK)

	t.Run("nobody", func(t *testing.T) {
		r := h.browser().me()
		if r.code != http.StatusUnauthorized || r.body != "{\"error\":\"sign_in\"}\n" ||
			r.header.Get("Content-Type") != "application/json" {
			t.Fatalf("%d %q %q", r.code, r.body, r.header.Get("Content-Type"))
		}
	})
	t.Run("the cookie", func(t *testing.T) {
		if r := signed.me(); r.code != http.StatusOK {
			t.Fatalf("%d %q", r.code, r.body)
		}
	})
	t.Run("the bearer token", func(t *testing.T) {
		if r := app.me(); r.code != http.StatusOK {
			t.Fatalf("%d %q", r.code, r.body)
		}
	})
	t.Run("a bad token is not rescued by a good cookie", func(t *testing.T) {
		b := h.browser()
		b.cookies[SessionCookie] = signed.cookies[SessionCookie]
		b.bearer = "not-a-token"
		if r := b.me(); r.code != http.StatusUnauthorized {
			t.Fatalf("%d %q", r.code, r.body)
		}
	})
	t.Run("a good token wins over a bad cookie", func(t *testing.T) {
		b := h.browser()
		b.cookies[SessionCookie] = "garbage"
		b.bearer = app.bearer
		if r := b.me(); r.code != http.StatusOK {
			t.Fatalf("%d %q", r.code, r.body)
		}
	})
	t.Run("other schemes and shapes", func(t *testing.T) {
		for _, auth := range []string{"Basic " + app.bearer, "Bearer", "Bearer ", "Bearer  x", "bearer " + app.bearer + "x", app.bearer} {
			b := h.browser()
			r := b.do(http.MethodGet, "/api/me", "", "Authorization", auth)
			if r.code != http.StatusUnauthorized {
				t.Errorf("Authorization %q: %d", auth, r.code)
			}
		}
		// The scheme is case-insensitive.
		r := h.browser().do(http.MethodGet, "/api/me", "", "Authorization", "bearer "+app.bearer)
		if r.code != http.StatusOK {
			t.Errorf("lowercase scheme: %d", r.code)
		}
	})
	t.Run("a database that does not answer is not a sign-out", func(t *testing.T) {
		mem := newMemSessions()
		h := newHarness(t, withSessions(mem))
		b := h.signInAs(testUserID)
		h.auth.forget(sha(b.cookies[SessionCookie])) // so the next call asks
		mem.mu.Lock()
		mem.err = errors.New("connection refused")
		mem.mu.Unlock()
		r := b.me()
		if r.code != http.StatusServiceUnavailable || r.body != "{\"error\":\"unavailable\"}\n" {
			t.Fatalf("%d %q", r.code, r.body)
		}
		// Person has no way to say "unknown", so it says nobody, and Require
		// above did not ask it.
		if _, ok := b.signedIn(); ok {
			t.Fatal("Person invented a person")
		}
		// Identify says it, for the job API to answer 503 as Require does.
		req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: b.cookies[SessionCookie]})
		if id, ok, err := h.auth.Identify(req); err == nil || ok || id != 0 {
			t.Errorf("Identify: %d %v %v", id, ok, err)
		}
		mem.mu.Lock()
		mem.err = nil
		mem.mu.Unlock()
		if id, ok, err := h.auth.Identify(req); err != nil || !ok || id != testUserID {
			t.Errorf("Identify once the database is back: %d %v %v", id, ok, err)
		}
	})
}

func TestPersonKeyIsTheUserIDAsText(t *testing.T) {
	h := newHarness(t)
	b := h.signInAs(testUserID)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := h.auth.PersonKey(req); got != "" {
		t.Errorf("nobody: %q", got)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: b.cookies[SessionCookie]})
	if got := h.auth.PersonKey(req); got != "4242" {
		t.Errorf("signed in: %q", got)
	}
}

// Person is one query the first time and a map lookup after; logging out and
// ending every session drop it at once; a row removed behind the process's
// back is believed for cacheTTL and no longer.
func TestPersonIsCached(t *testing.T) {
	mem := newMemSessions()
	h := newHarness(t, withSessions(mem))
	b := h.signInAs(testUserID)

	base := mem.lookupCount()
	for i := 0; i < 5; i++ {
		if id, ok := b.signedIn(); !ok || id != testUserID {
			t.Fatalf("round %d: %d %v", i, id, ok)
		}
	}
	if n := mem.lookupCount() - base; n != 1 {
		t.Errorf("5 calls made %d queries, want 1", n)
	}

	h.clock.Advance(cacheTTL + time.Second)
	b.signedIn()
	if n := mem.lookupCount() - base; n != 2 {
		t.Errorf("after the cache expired: %d queries, want 2", n)
	}

	// A row deleted by something else is still believed until the cache runs
	// out, and not after.
	mem.mu.Lock()
	delete(mem.rows, sha(b.cookies[SessionCookie]))
	mem.mu.Unlock()
	if _, ok := b.signedIn(); !ok {
		t.Error("the cache was not used")
	}
	h.clock.Advance(cacheTTL + time.Second)
	if _, ok := b.signedIn(); ok {
		t.Error("a deleted session is believed past cacheTTL")
	}

}

// A session another process ended is not believed from memory once that
// process says so, and the people whose sessions stand are looked up again.
func TestForgetAllDropsWhatAnotherProcessEnded(t *testing.T) {
	mem := newMemSessions()
	h := newHarness(t, withSessions(mem))
	ended, kept := h.signInAs(testUserID), h.signInAs(7)
	ended.signedIn()
	kept.signedIn() // both cached
	mem.mu.Lock()
	delete(mem.rows, sha(ended.cookies[SessionCookie]))
	mem.mu.Unlock()

	h.auth.ForgetAll()
	base := mem.lookupCount()
	if _, ok := ended.signedIn(); ok {
		t.Error("a session ended elsewhere is still believed")
	}
	if id, ok := kept.signedIn(); !ok || id != 7 {
		t.Errorf("a live session went with it: %d %v", id, ok)
	}
	if n := mem.lookupCount() - base; n != 2 {
		t.Errorf("%d lookups after forgetting, want 2", n)
	}
}

func TestEndedSessionIsGoneAtOnceDespiteTheCache(t *testing.T) {
	h := newHarness(t)
	a, b := h.signInAs(testUserID), h.signInAs(7)
	oldA := a.cookies[SessionCookie]
	a.signedIn()
	b.signedIn() // both cached
	if r := a.do(http.MethodPost, "/auth/logout", ""); r.code != http.StatusNoContent {
		t.Fatalf("logout: %d", r.code)
	}
	a.cookies[SessionCookie] = oldA
	if _, ok := a.signedIn(); ok {
		t.Error("a logged-out session is still believed")
	}
	if _, ok := b.signedIn(); !ok {
		t.Error("logging one person out ended another's session")
	}
}

func TestSignOutEverywhere(t *testing.T) {
	h := newHarness(t)
	laptop := h.signInAs(testUserID)
	phone := h.signInAs(testUserID)
	app := h.browser()
	h.mini(app, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusOK)
	someoneElse := h.signInAs(7)
	for _, b := range []*browser{laptop, phone, app, someoneElse} {
		if _, ok := b.signedIn(); !ok {
			t.Fatal("not signed in to begin with")
		}
	}
	old := laptop.cookies[SessionCookie]

	if r := h.browser().do(http.MethodPost, "/auth/logout/all", ""); r.code != http.StatusUnauthorized {
		t.Fatalf("logout/all by nobody: %d", r.code)
	}
	r := laptop.do(http.MethodPost, "/auth/logout/all", "")
	if r.code != http.StatusNoContent {
		t.Fatalf("logout/all: %d %q", r.code, r.body)
	}
	laptop.cookies[SessionCookie] = old
	for name, b := range map[string]*browser{"laptop": laptop, "phone": phone, "app": app} {
		if _, ok := b.signedIn(); ok {
			t.Errorf("%s is still signed in", name)
		}
	}
	if id, ok := someoneElse.signedIn(); !ok || id != 7 {
		t.Error("another person was signed out")
	}
	if n := h.sessions.(*memSessions).count(); n != 1 {
		t.Errorf("%d rows left, want 1", n)
	}

	// The same through the Go API, for the code that deletes an account.
	if n, err := h.auth.RevokeAll(t.Context(), 7); err != nil || n != 1 {
		t.Fatalf("RevokeAll = %d, %v", n, err)
	}
	if _, ok := someoneElse.signedIn(); ok {
		t.Error("RevokeAll left a session believed")
	}
}

// A lookup that began before a revocation must not put its answer in the
// cache after it.
func TestRevocationDuringALookupIsNotUndone(t *testing.T) {
	mem := newMemSessions()
	h := newHarness(t, withSessions(mem))
	b := h.signInAs(testUserID)
	cookie := b.cookies[SessionCookie]
	h.auth.forget(sha(cookie)) // start with nothing cached

	read, release := make(chan struct{}), make(chan struct{})
	mem.mu.Lock()
	mem.inLookup = func() {
		close(read)
		<-release
	}
	mem.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	var gotOK bool
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
		_, gotOK = h.auth.Person(req) // read the row, then waits
	}()
	<-read
	mem.mu.Lock()
	mem.inLookup = nil
	mem.mu.Unlock()
	if _, err := h.auth.RevokeAll(t.Context(), testUserID); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if !gotOK {
		t.Log("the racing lookup was told nobody; also fine")
	}

	before := mem.lookupCount()
	if _, ok := b.signedIn(); ok {
		t.Fatal("the session came back")
	}
	if mem.lookupCount() == before {
		t.Error("the answer of the lookup that raced the revocation was cached")
	}
}

// Sweep removes what expired a day ago and keeps the rest, whether or not
// anyone signs in.
func TestSweepRemovesExpiredSessions(t *testing.T) {
	mem := newMemSessions()
	h := newHarness(t, withSessions(mem))
	h.signInAs(testUserID)
	h.signInAs(7)
	if mem.count() != 2 {
		t.Fatal("setup")
	}
	h.clock.Advance(SessionLife + time.Hour)
	h.signInAs(8)
	// Expired an hour ago: kept a day, for whoever looks at why.
	if n, err := h.auth.Sweep(context.Background()); err != nil || n != 0 || mem.count() != 3 {
		t.Fatalf("Sweep = %d, %v with %d rows; want nothing removed yet", n, err, mem.count())
	}
	h.clock.Advance(sweepGrace)
	if n, err := h.auth.Sweep(context.Background()); err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v; want the two expired", n, err)
	}
	if mem.count() != 1 {
		t.Errorf("%d rows after the sweep, want the live one", mem.count())
	}
}

func TestLogoutWithNothingIsStillSignedOut(t *testing.T) {
	h := newHarness(t)
	r := h.browser().do(http.MethodPost, "/auth/logout", "")
	if r.code != http.StatusNoContent {
		t.Fatalf("%d", r.code)
	}
	if c := r.cookie(SessionCookie); c == nil || c.MaxAge >= 0 {
		t.Error("the cookie is not cleared")
	}
}

func TestLogoutThatFailsKeepsTheCookie(t *testing.T) {
	mem := newMemSessions()
	h := newHarness(t, withSessions(mem))
	b := h.signInAs(testUserID)
	mem.mu.Lock()
	mem.err = errors.New("connection refused")
	mem.mu.Unlock()
	r := b.do(http.MethodPost, "/auth/logout", "")
	if r.code != http.StatusServiceUnavailable || r.cookie(SessionCookie) != nil {
		t.Fatalf("%d, cookie %v", r.code, r.header.Values("Set-Cookie"))
	}
	mem.mu.Lock()
	mem.err = nil
	mem.mu.Unlock()
	if _, ok := b.signedIn(); !ok {
		t.Error("the session ended although the delete failed")
	}
}

func TestBearerLogoutEndsTheToken(t *testing.T) {
	h := newHarness(t)
	app := h.browser()
	token := h.mini(app, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusOK)
	if r := app.do(http.MethodPost, "/auth/logout", ""); r.code != http.StatusNoContent {
		t.Fatalf("%d", r.code)
	}
	app.bearer = token
	if _, ok := app.signedIn(); ok {
		t.Error("the token outlived its logout")
	}
}

func sha(secret string) [32]byte {
	h, _ := hashToken(secret)
	return h
}

// Signing in again ends the session the browser had: a copy of the old
// cookie stops working, and one person signing in many times keeps one
// session, not one per sign-in. A sign-in that fails leaves the old one.
func TestSigningInAgainEndsTheOldSession(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.signIn("")
	old := b.cookies[SessionCookie]
	if _, ok := b.signedIn(); !ok {
		t.Fatal("not signed in")
	}

	h.idp.mu.Lock()
	h.idp.deny = true
	h.idp.mu.Unlock()
	b.signIn("")
	if b.cookies[SessionCookie] != old {
		t.Fatal("a refused sign-in replaced the cookie")
	}
	if _, ok := b.signedIn(); !ok {
		t.Fatal("a refused sign-in ended the session the browser had")
	}

	h.idp.mu.Lock()
	h.idp.deny = false
	h.idp.mu.Unlock()
	b.signIn("")
	if b.cookies[SessionCookie] == old {
		t.Fatal("the same session after signing in again")
	}
	if _, ok := b.signedIn(); !ok {
		t.Fatal("not signed in with the new session")
	}
	if n := h.sessions.(*memSessions).count(); n != 1 {
		t.Errorf("%d sessions after signing in twice", n)
	}
	copied := h.browser()
	copied.cookies[SessionCookie] = old
	if _, ok := copied.signedIn(); ok {
		t.Error("a copy of the old cookie still signs in")
	}
}
