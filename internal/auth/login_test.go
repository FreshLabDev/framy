// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"
)

func mustJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("not JSON: %v: %q", err, s)
	}
}

// loginStateOf decodes the browser's login cookie.
func loginStateOf(t *testing.T, b *browser) loginState {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(b.cookies[loginCookie])
	if err != nil {
		t.Fatalf("login cookie: %v", err)
	}
	var st loginState
	mustJSON(t, string(raw), &st)
	return st
}

// setLoginState writes a login cookie into the browser.
func setLoginState(b *browser, st loginState) {
	raw, _ := json.Marshal(st)
	b.cookies[loginCookie] = base64.RawURLEncoding.EncodeToString(raw)
}

func TestSignInThenSignOut(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	if _, ok := b.signedIn(); ok {
		t.Fatal("a new browser is signed in")
	}
	if r := b.me(); r.code != http.StatusUnauthorized {
		t.Fatalf("/api/me before sign-in: %d", r.code)
	}

	cb := b.signIn("/models?sort=price")
	if cb.code != http.StatusSeeOther || cb.location() != "/models?sort=price" {
		t.Fatalf("callback: %d -> %q", cb.code, cb.location())
	}
	if id, ok := b.signedIn(); !ok || id != testUserID {
		t.Fatalf("after sign-in: %d, %v", id, ok)
	}
	if r := b.me(); r.code != http.StatusOK || r.body != "{\"id\":4242}\n" {
		t.Fatalf("/api/me: %d %q", r.code, r.body)
	}
	if got := h.people.(*memPeople).users(); len(got) != 1 || got[0] != (tg.User{ID: testUserID, FirstName: "Olesya", Username: "olesya_u"}) {
		t.Fatalf("core.touch got %+v", got)
	}

	old := b.cookies[SessionCookie]
	out := b.do(http.MethodPost, "/auth/logout", "")
	if out.code != http.StatusNoContent {
		t.Fatalf("logout: %d %q", out.code, out.body)
	}
	if c := out.cookie(SessionCookie); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("logout did not clear the cookie: %v", out.header.Values("Set-Cookie"))
	}
	if _, ok := b.signedIn(); ok {
		t.Fatal("still signed in after logout")
	}
	if n := h.sessions.(*memSessions).count(); n != 0 {
		t.Fatalf("%d sessions are left", n)
	}
	// A copy of the old cookie is worth nothing: the session is gone, not
	// just forgotten by this browser.
	b.cookies[SessionCookie] = old
	if _, ok := b.signedIn(); ok {
		t.Fatal("the old cookie still signs in")
	}
}

func TestAuthorizationRequestAndTokenRequest(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	authURL := b.startLogin("")
	callback := b.approve(authURL)
	q := h.idp.lastAuthRequest()

	if q.Get("response_type") != "code" || q.Get("client_id") != testClientID ||
		q.Get("redirect_uri") != testSiteURL+"/auth/callback" {
		t.Errorf("authorization request: %v", q)
	}
	if q.Get("scope") != "openid profile telegram:bot_access" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
	st := loginStateOf(t, b)
	if q.Get("state") != st.State || q.Get("nonce") != st.Nonce || len(st.State) < 40 || len(st.Nonce) < 40 || st.State == st.Nonce {
		t.Errorf("state and nonce: request %v, cookie %+v", q, st)
	}
	sum := sha256.Sum256([]byte(st.Verifier))
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Errorf("PKCE: method %q, challenge %q, verifier %q", q.Get("code_challenge_method"), q.Get("code_challenge"), st.Verifier)
	}
	if strings.Contains(authURL, st.Verifier) || strings.Contains(authURL, testClientSecret) {
		t.Errorf("the authorization URL carries a secret: %s", authURL)
	}

	if cb := b.get(callback); cb.code != http.StatusSeeOther || cb.location() != "/" {
		t.Fatalf("callback: %d -> %q", cb.code, cb.location())
	}
	h.idp.mu.Lock()
	defer h.idp.mu.Unlock()
	if len(h.idp.tokenRequests) != 1 {
		t.Fatalf("%d token requests", len(h.idp.tokenRequests))
	}
	tr := h.idp.tokenRequests[0]
	// AuthStyleInHeader: the client authenticates in the Authorization header
	// and its secret never goes in the body.
	if !tr.basicOK || tr.bodyHasSecret {
		t.Errorf("client authentication: header %v, secret in body %v", tr.basicOK, tr.bodyHasSecret)
	}
	if tr.form.Get("code_verifier") != st.Verifier || tr.form.Get("grant_type") != "authorization_code" ||
		tr.form.Get("redirect_uri") != testSiteURL+"/auth/callback" {
		t.Errorf("token request form: %v", tr.form)
	}
}

// A provider that turns the client away once must not be answered with the
// secret in the body: that is what auto-detecting the authentication style
// would do, and why it is set outright.
func TestClientSecretNeverGoesInTheBody(t *testing.T) {
	h := newHarness(t)
	h.idp.rejectTokens = 1
	b := h.browser()
	cb := b.signIn("")
	if cb.location() != "/?sign_in=failed" {
		t.Fatalf("callback went to %q", cb.location())
	}
	h.idp.mu.Lock()
	defer h.idp.mu.Unlock()
	if len(h.idp.tokenRequests) != 1 {
		t.Errorf("%d token requests, want 1: the client does not retry another way", len(h.idp.tokenRequests))
	}
	for _, tr := range h.idp.tokenRequests {
		if tr.bodyHasSecret || !tr.basicOK {
			t.Errorf("a token request authenticated the client in the body: %+v", tr)
		}
	}
}

func TestLoginCookieAttributes(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	r := b.get("/auth/login")
	line := r.rawSetCookie(loginCookie)
	for _, want := range []string{"Path=/", "Max-Age=600", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(line, want) {
			t.Errorf("login cookie %q lacks %s", line, want)
		}
	}
	if strings.Contains(line, "Domain") {
		t.Errorf("login cookie %q has a Domain, which `__Host-` forbids", line)
	}
	// What it holds stays in the browser: Telegram sees only the challenge.
	if loc := r.location(); strings.Contains(loc, loginStateOf(t, b).Verifier) {
		t.Error("the verifier went to Telegram")
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	cb := b.signIn("")
	line := cb.rawSetCookie(SessionCookie)
	if line == "" {
		t.Fatal("no session cookie")
	}
	for _, want := range []string{SessionCookie + "=", "Path=/", "Max-Age=2592000", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(line, want) {
			t.Errorf("session cookie %q lacks %s", line, want)
		}
	}
	if strings.Contains(line, "Domain") {
		t.Errorf("session cookie %q has a Domain, which `__Host-` forbids", line)
	}
	// 256 random bits, as base64url.
	if v := b.cookies[SessionCookie]; len(v) != 43 {
		t.Errorf("session id %q is %d characters", v, len(v))
	}
	// The login cookie is spent.
	if c := cb.cookie(loginCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("the login cookie is not cleared: %v", cb.header.Values("Set-Cookie"))
	}
	if _, ok := b.cookies[loginCookie]; ok {
		t.Error("the browser still holds the login cookie")
	}
}

func TestOnlyTheHashOfASessionIsStored(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.signIn("")
	secret := b.cookies[SessionCookie]
	want := sha256.Sum256([]byte(secret))

	rows := h.sessions.(*memSessions).rows
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	s, ok := rows[want]
	if !ok {
		t.Fatal("the row is not keyed by the SHA-256 of the cookie")
	}
	// Nothing in the row is the secret in any form.
	dump := string(s.Hash[:]) + string(s.Kind) + s.Created.String() + s.Expires.String()
	for _, form := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), hex.EncodeToString([]byte(secret))} {
		if strings.Contains(dump, form) {
			t.Errorf("the row holds %q", form)
		}
	}
	if s.UserID != testUserID || s.Kind != KindSite || s.Expires.Sub(s.Created) != SessionLife {
		t.Errorf("row = %+v", s)
	}
}

func TestSessionExpires(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.signIn("")
	h.clock.Advance(SessionLife - time.Minute)
	if _, ok := b.signedIn(); !ok {
		t.Fatal("signed out before the session was over")
	}
	h.clock.Advance(2 * time.Minute)
	if _, ok := b.signedIn(); ok {
		t.Fatal("a session older than SessionLife is accepted")
	}
}

func TestTheAccountIsTheIDClaimNotSub(t *testing.T) {
	for name, tc := range map[string]struct {
		userID any
		sub    string
		want   int64
	}{
		"sub is something else":       {int64(4242), "opaque-sub", 4242},
		"sub is another number":       {int64(4242), "1337", 4242},
		"id as a string of digits":    {"4242", "x", 4242},
		"a large id keeps its digits": {int64(7_000_000_000_001), "x", 7_000_000_000_001},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.idp.userID, h.idp.sub = tc.userID, tc.sub
			b := h.browser()
			b.signIn("")
			if id, ok := b.signedIn(); !ok || id != tc.want {
				t.Fatalf("signed in as %d (%v), want %d", id, ok, tc.want)
			}
			if got := h.people.(*memPeople).users(); len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("touched %+v", got)
			}
		})
	}
}

// A token with no id claim is no sign-in, whatever sub holds.
func TestSubIsNeverTheAccount(t *testing.T) {
	h := newHarness(t)
	h.idp.claims = func(c map[string]any) {
		delete(c, "id")
		c["sub"] = "4242"
	}
	b := h.browser()
	cb := b.signIn("")
	if cb.location() != "/?sign_in=failed" {
		t.Fatalf("callback went to %q", cb.location())
	}
	if _, ok := b.signedIn(); ok || h.sessions.(*memSessions).count() != 0 {
		t.Fatal("signed in on sub")
	}
	if !strings.Contains(h.logText(), "result=site_claims") {
		t.Errorf("not refused as a claims problem:\n%s", h.logText())
	}
}

func TestNamesAndLanguageForCoreTouch(t *testing.T) {
	for name, tc := range map[string]struct {
		claims func(map[string]any)
		want   tg.User
	}{
		"one word is a first name": {
			func(c map[string]any) { c["name"] = "Olesya" },
			tg.User{ID: testUserID, FirstName: "Olesya", Username: "olesya_u"},
		},
		"two words are not split, so the bot's names are not overwritten": {
			func(c map[string]any) { c["name"] = "Mary Ann Smith" },
			tg.User{ID: testUserID, Username: "olesya_u"},
		},
		"given and family names are used": {
			func(c map[string]any) {
				c["name"] = "Mary Ann Smith"
				c["given_name"] = "Mary Ann"
				c["family_name"] = "Smith"
			},
			tg.User{ID: testUserID, FirstName: "Mary Ann", LastName: "Smith", Username: "olesya_u"},
		},
		"a locale becomes a language code": {
			func(c map[string]any) { c["locale"] = "uk_UA" },
			tg.User{ID: testUserID, FirstName: "Olesya", Username: "olesya_u", LanguageCode: "uk"},
		},
		"nonsense in the locale is dropped": {
			func(c map[string]any) { c["locale"] = "x" },
			tg.User{ID: testUserID, FirstName: "Olesya", Username: "olesya_u"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.idp.claims = tc.claims
			h.browser().signIn("")
			if got := h.people.(*memPeople).users(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("touched %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRefusedIDTokens(t *testing.T) {
	now := func(h *harness) time.Time { return h.clock.Now() }
	type tc struct {
		name   string
		claims func(h *harness, c map[string]any)
		issue  func(t *testing.T, c map[string]any) string
		result string // the metric and log label the refusal must carry
	}
	cases := []tc{
		{name: "payload edited after signing", result: "site_token",
			issue: func(t *testing.T, c map[string]any) string {
				return editPayload(signRS256(t, c), func(m map[string]any) { m["id"] = int64(1) })
			}},
		{name: "signed by a key Telegram does not publish", result: "site_token",
			issue: func(t *testing.T, c map[string]any) string { return signWithOtherKey(t, c) }},
		{name: "expired", result: "site_token",
			claims: func(h *harness, c map[string]any) { c["exp"] = now(h).Add(-time.Minute).Unix() }},
		{name: "another client's audience", result: "site_token",
			claims: func(h *harness, c map[string]any) { c["aud"] = "999999" }},
		{name: "another issuer", result: "site_token",
			claims: func(h *harness, c map[string]any) { c["iss"] = "https://evil.example" }},
		{name: "RS512, which is not RS256", result: "site_token",
			issue: func(t *testing.T, c map[string]any) string { return signRS512(t, c) }},
		{name: "HS256 keyed with the public key", result: "site_token",
			issue: func(t *testing.T, c map[string]any) string { return signHS256WithPublicKey(t, c) }},
		{name: "alg none", result: "site_token",
			issue: func(t *testing.T, c map[string]any) string { return signNone(c) }},
		{name: "truncated signature", result: "site_token",
			issue: func(t *testing.T, c map[string]any) string { s := signRS256(t, c); return s[:len(s)-8] }},
		{name: "the nonce of another login", result: "site_nonce",
			claims: func(h *harness, c map[string]any) { c["nonce"] = "the-nonce-of-someone-else" }},
		{name: "no nonce", result: "site_nonce",
			claims: func(h *harness, c map[string]any) { delete(c, "nonce") }},
		{name: "no id", result: "site_claims",
			claims: func(h *harness, c map[string]any) { delete(c, "id") }},
		{name: "id zero", result: "site_claims",
			claims: func(h *harness, c map[string]any) { c["id"] = 0 }},
		{name: "id negative", result: "site_claims",
			claims: func(h *harness, c map[string]any) { c["id"] = -5 }},
		{name: "id a fraction", result: "site_claims",
			claims: func(h *harness, c map[string]any) { c["id"] = 4242.5 }},
		{name: "id a word", result: "site_claims",
			claims: func(h *harness, c map[string]any) { c["id"] = "olesya" }},
		{name: "id null", result: "site_claims",
			claims: func(h *harness, c map[string]any) { c["id"] = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if c.claims != nil {
				h.idp.claims = func(m map[string]any) { c.claims(h, m) }
			}
			if c.issue != nil {
				h.idp.issue = func(m map[string]any) string { return c.issue(t, m) }
			}
			b := h.browser()
			cb := b.signIn("/after")
			if cb.code != http.StatusSeeOther || cb.location() != "/?sign_in=failed" {
				t.Fatalf("callback: %d -> %q", cb.code, cb.location())
			}
			if cb.cookie(SessionCookie) != nil {
				t.Error("a session cookie was set")
			}
			if _, ok := b.signedIn(); ok || h.sessions.(*memSessions).count() != 0 {
				t.Error("signed in on a refused token")
			}
			if n := len(h.people.(*memPeople).users()); n != 0 {
				t.Errorf("core.touch ran %d times for a refused token", n)
			}
			if !strings.Contains(h.logText(), "result="+c.result) {
				t.Errorf("refused, but not as %s:\n%s", c.result, h.logText())
			}
		})
	}
}

func TestStateAndCookieChecks(t *testing.T) {
	t.Run("state of another login", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser()
		cb, _ := url.Parse(b.approve(b.startLogin("")))
		q := cb.Query()
		q.Set("state", "the-state-of-an-attackers-login")
		cb.RawQuery = q.Encode()
		r := b.get(cb.String())
		expectRefused(t, h, b, r, "failed", "site_state")
		// Refused before the code is spent or the token endpoint is called.
		if h.idp.tokenRequestCount() != 0 {
			t.Error("the code was traded despite the wrong state")
		}
	})
	t.Run("no state at all", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser()
		cb, _ := url.Parse(b.approve(b.startLogin("")))
		q := cb.Query()
		q.Del("state")
		cb.RawQuery = q.Encode()
		expectRefused(t, h, b, b.get(cb.String()), "failed", "site_state")
	})
	t.Run("no login cookie", func(t *testing.T) {
		h := newHarness(t)
		victim := h.browser()
		// The attacker's browser started the login; the victim is tricked into
		// opening the callback.
		attacker := h.browser()
		cb := attacker.approve(attacker.startLogin(""))
		expectRefused(t, h, victim, victim.get(cb), "expired", "site_no_state")
		if h.idp.tokenRequestCount() != 0 {
			t.Error("the code was traded without a login cookie")
		}
	})
	t.Run("another login in the same browser", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser()
		first := b.approve(b.startLogin(""))
		b.startLogin("") // a second tab replaces the cookie
		expectRefused(t, h, b, b.get(first), "failed", "site_state")
	})
	t.Run("login cookie older than ten minutes", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser()
		cb := b.approve(b.startLogin(""))
		h.clock.Advance(loginLife + time.Second)
		expectRefused(t, h, b, b.get(cb), "expired", "site_no_state")
	})
	t.Run("PKCE verifier of another login", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser()
		cb := b.approve(b.startLogin(""))
		st := loginStateOf(t, b)
		st.Verifier = "a-verifier-that-never-made-the-challenge-0123456789"
		setLoginState(b, st)
		expectRefused(t, h, b, b.get(cb), "failed", "site_exchange")
	})
	t.Run("the callback used twice", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser()
		cb := b.approve(b.startLogin(""))
		st := loginStateOf(t, b)
		if r := b.get(cb); r.code != http.StatusSeeOther || r.location() != "/" {
			t.Fatalf("first use: %d -> %q", r.code, r.location())
		}
		// The cookie is spent, so a replay from the same browser has none...
		expectNoSecondSession(t, h, b.get(cb), "expired", "site_no_state")
		// ... and a replay with a copy of it meets a code the provider has used.
		replay := h.browser()
		setLoginState(replay, st)
		expectRefused(t, h, replay, replay.get(cb), "failed", "site_exchange")
		if n := h.sessions.(*memSessions).count(); n != 1 {
			t.Errorf("%d sessions after a replay", n)
		}
	})
	t.Run("the person pressed Cancel", func(t *testing.T) {
		h := newHarness(t)
		h.idp.deny = true
		b := h.browser()
		expectRefused(t, h, b, b.signIn(""), "cancelled", "site_denied")
	})
}

// expectNoSecondSession is expectRefused for a browser that is already signed
// in from the first use: the answer is a refusal and no new session.
func expectNoSecondSession(t *testing.T, h *harness, r resp, reason, result string) {
	t.Helper()
	if r.code != http.StatusSeeOther || r.location() != "/?sign_in="+reason || r.cookie(SessionCookie) != nil {
		t.Errorf("callback: %d -> %q, cookies %v", r.code, r.location(), r.header.Values("Set-Cookie"))
	}
	if !strings.Contains(h.logText(), "result="+result) {
		t.Errorf("not refused as %s:\n%s", result, h.logText())
	}
}

// expectRefused checks a callback that must not sign anyone in.
func expectRefused(t *testing.T, h *harness, b *browser, r resp, reason, result string) {
	t.Helper()
	if r.code != http.StatusSeeOther || r.location() != "/?sign_in="+reason {
		t.Errorf("callback: %d -> %q, want /?sign_in=%s", r.code, r.location(), reason)
	}
	if r.cookie(SessionCookie) != nil {
		t.Error("a session cookie was set")
	}
	if _, ok := b.signedIn(); ok {
		t.Error("signed in")
	}
	if !strings.Contains(h.logText(), "result="+result) {
		t.Errorf("not refused as %s:\n%s", result, h.logText())
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "/",
		"/":                            "/",
		"/models":                      "/models",
		"/models?sort=price&x=1":       "/models?sort=price&x=1",
		"/a/b#frag":                    "/a/b#frag",
		"/%2F/evil.example":            "/%2F/evil.example", // a path on this site, not a host
		"//evil.example":               "/",
		"//evil.example/path":          "/",
		"/\\evil.example":              "/",
		"\\\\evil.example":             "/",
		"https://evil.example":         "/",
		"http://evil.example/x":        "/",
		"evil.example":                 "/",
		"javascript:alert(1)":          "/",
		"/\t/evil.example":             "/",
		"/\n/evil.example":             "/",
		"/ /evil.example":              "/",
		"/\x00":                        "/",
		"/дом":                         "/",
		"/auth/login":                  "/",
		"/auth":                        "/",
		"/auth/logout":                 "/",
		"/authors":                     "/authors",
		"/" + strings.Repeat("a", 600): "/",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenRedirectsAreRefused(t *testing.T) {
	for _, evil := range []string{"//evil.example", "https://evil.example/", "/\\evil.example", "javascript:alert(1)"} {
		t.Run(evil, func(t *testing.T) {
			h := newHarness(t)
			b := h.browser()
			if cb := b.signIn(evil); cb.code != http.StatusSeeOther || cb.location() != "/" {
				t.Errorf("return_to %q: the callback went to %q", evil, cb.location())
			}
		})
	}
	// The cookie is the browser's to edit; the callback believes it no more
	// than the query.
	h := newHarness(t)
	b := h.browser()
	cb := b.approve(b.startLogin("/fine"))
	st := loginStateOf(t, b)
	st.ReturnTo = "//evil.example"
	setLoginState(b, st)
	if r := b.get(cb); r.code != http.StatusSeeOther || r.location() != "/" {
		t.Errorf("a doctored cookie sent the browser to %q", r.location())
	}
}

func TestSiteSignInNeedsTheClient(t *testing.T) {
	h := newHarness(t, withoutClient())
	if h.auth.SiteEnabled() {
		t.Fatal("enabled without a client")
	}
	b := h.browser()
	for _, target := range []string{"/auth/login", "/auth/callback?code=x&state=y"} {
		r := b.get(target)
		if r.code != http.StatusServiceUnavailable || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %d %q", target, r.code, r.header.Get("Content-Type"))
		}
		if r.cookie(loginCookie) != nil || r.location() != "" {
			t.Errorf("%s set a cookie or redirected", target)
		}
	}
	// The rest runs: the Mini App does not need the Login client.
	r := b.get("/api/open")
	if r.code != http.StatusOK {
		t.Errorf("/api/open: %d", r.code)
	}
	h.mini(b, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusOK)
	if id, ok := b.signedIn(); !ok || id != testUserID {
		t.Errorf("the Mini App cannot sign in: %d %v", id, ok)
	}
}

func TestNewRefusesHalfAClientAndABadSiteURL(t *testing.T) {
	base := Config{BotToken: testBotToken, Sessions: newMemSessions(), People: &memPeople{}}
	for name, mod := range map[string]func(*Config){
		"id without secret":     func(c *Config) { c.ClientID = "1" },
		"secret without id":     func(c *Config) { c.ClientSecret = "s" },
		"client without a site": func(c *Config) { c.ClientID, c.ClientSecret = "1", "s" },
		"http site":             func(c *Config) { c.ClientID, c.ClientSecret, c.SiteURL = "1", "s", "http://framy.example" },
		"site with a path":      func(c *Config) { c.ClientID, c.ClientSecret, c.SiteURL = "1", "s", "https://framy.example/app" },
		"no bot token":          func(c *Config) { c.BotToken = "" },
		"no sessions":           func(c *Config) { c.Sessions = nil },
		"no people":             func(c *Config) { c.People = nil },
	} {
		cfg := base
		mod(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	cfg := base
	cfg.ClientID, cfg.ClientSecret, cfg.SiteURL = "1", "s", "http://localhost:8080/"
	if _, err := New(cfg); err != nil {
		t.Errorf("localhost over http: %v", err)
	}
}

func TestDiscoveryFailureIsNotRemembered(t *testing.T) {
	h := newHarness(t)
	h.idp.mu.Lock()
	h.idp.discovery = http.StatusBadGateway
	h.idp.mu.Unlock()
	b := h.browser()
	if r := b.get("/auth/login"); r.code != http.StatusServiceUnavailable || r.cookie(loginCookie) != nil {
		t.Fatalf("with Telegram down: %d, cookie %v", r.code, r.cookie(loginCookie))
	}
	h.idp.mu.Lock()
	h.idp.discovery = 0
	h.idp.mu.Unlock()
	if cb := b.signIn(""); cb.code != http.StatusSeeOther || cb.location() != "/" {
		t.Fatalf("after Telegram came back: %d -> %q", cb.code, cb.location())
	}
	if _, ok := b.signedIn(); !ok {
		t.Fatal("not signed in")
	}
}

func TestSignInRoutesAreRateLimitedAndNotCached(t *testing.T) {
	h := newHarness(t, withRealLimits())
	b := h.browser()
	var last resp
	got429 := -1
	for i := 0; i < 30; i++ {
		last = b.get("/auth/login")
		if last.code == http.StatusTooManyRequests {
			got429 = i
			break
		}
		if i == 0 {
			if cc := last.header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q", cc)
			}
			if last.header.Get("Referrer-Policy") != "no-referrer" || last.header.Get("Cross-Origin-Opener-Policy") != "same-origin" {
				t.Errorf("headers: %v", last.header)
			}
		}
	}
	// The auth class: a burst of 10 per address.
	if got429 != 10 {
		t.Errorf("the 429 came at request %d, want 10 (the auth class's burst)", got429)
	}
	if last.header.Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
}

// Nothing secret reaches a log line, in the good case or the bad.
func TestNoSecretsInLogs(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	// A failure, then a success, then a Mini App exchange.
	h.idp.claims = func(c map[string]any) { c["nonce"] = "wrong" }
	b.signIn("")
	failedToken := h.idp.lastIDToken()
	h.idp.claims = nil
	b.signIn("")
	idToken := h.idp.lastIDToken()
	session := b.cookies[SessionCookie]
	raw := signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID))
	token := h.mini(b, raw, http.StatusOK)
	h.mini(b, raw+"0", http.StatusUnauthorized)

	logs := h.logText()
	if logs == "" {
		t.Fatal("nothing was logged at all")
	}
	forbidden := []string{
		testClientSecret, testBotToken, "access-token-DO-NOT-LOG", idToken, failedToken, session, token, raw,
		b.cookies[loginCookie],
	}
	h.idp.mu.Lock()
	for _, tr := range h.idp.tokenRequests {
		forbidden = append(forbidden, tr.form.Get("code"), tr.form.Get("code_verifier"))
	}
	h.idp.mu.Unlock()
	for _, s := range forbidden {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("the log holds a secret: %q", s)
		}
	}
	// Telegram's refusal of a code is logged as a code, not as its body.
	if strings.Contains(logs, "error_description") {
		t.Errorf("the provider's response body is in the log:\n%s", logs)
	}
}

// A sign-in keeps the name and the photo Telegram gave, for the site's
// header, and one whose card cannot be written still signs the person in.
func TestSignInKeepsTheNameAndPhoto(t *testing.T) {
	h := newHarness(t)
	h.idp.mu.Lock()
	h.idp.claims = func(c map[string]any) {
		c["name"] = "Olesya Kovalenko"
		c["picture"] = "https://t.me/i/userpic/320/olesya_u.jpg"
	}
	h.idp.mu.Unlock()
	b := h.browser()
	b.signIn("/")
	if got := h.people.(*memPeople).card(testUserID); got != [2]string{"Olesya Kovalenko", "https://t.me/i/userpic/320/olesya_u.jpg"} {
		t.Fatalf("card = %q", got)
	}

	h2 := newHarness(t)
	h2.people.(*memPeople).cardErr = errors.New("the database is gone")
	b2 := h2.browser()
	b2.signIn("/")
	if _, ok := b2.signedIn(); !ok {
		t.Fatal("a card that could not be written stopped the sign-in")
	}
}
