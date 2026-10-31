// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/metrics"
)

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// metric reads a counter from the exposition text, since the counters keep
// their values private; label is "" for a counter without one.
func metric(t *testing.T, name, label string) int64 {
	t.Helper()
	var buf strings.Builder
	metrics.Write(&buf)
	want := name + " "
	if label != "" {
		want = name + `{result="` + label + `"} `
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, want); ok {
			n, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	return 0
}

// RFC 7636, appendix B: the verifier and the challenge it must give.
func TestChallengeIsS256(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := challenge(verifier); got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestTokensAreRandomAndFitTheVerifierAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		tok, err := randomToken()
		if err != nil || !tokenShape.MatchString(tok) || seen[tok] {
			t.Fatalf("token %q, %v (seen before: %v)", tok, err, seen[tok])
		}
		seen[tok] = true
	}
}

func TestStartSendsThePersonToOpenRouterWithAnS256Challenge(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	before := metric(t, "framy_oauth_started_total", "")

	rr := b.get(StartPath)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	loc, err := url.Parse(rr.Header().Get("Location"))
	if err != nil || loc.Scheme+"://"+loc.Host+loc.Path != h.or.URL+"/auth" {
		t.Fatalf("redirects to %q", rr.Header().Get("Location"))
	}
	q := loc.Query()
	state := q.Get("state")
	if !tokenShape.MatchString(state) {
		t.Errorf("state = %q", state)
	}
	if q.Get("callback_url") != siteOrigin+"/oauth/openrouter/callback" {
		t.Errorf("callback_url = %q", q.Get("callback_url"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("key_label") != "framy" {
		t.Errorf("key_label = %q", q.Get("key_label"))
	}

	// The challenge is the S256 of the verifier the store holds, and the
	// verifier is not the state, nor in any answer of the site.
	flow := h.store.peek(t, state)
	if q.Get("code_challenge") != s256(flow.Verifier) || !tokenShape.MatchString(flow.Verifier) || flow.Verifier == state {
		t.Errorf("challenge %q does not belong to the verifier of the flow", q.Get("code_challenge"))
	}
	if flow.UserID != ann || flow.Provider != "openrouter" {
		t.Errorf("flow = %+v", flow)
	}
	if want := h.clock.Now().Add(10 * time.Minute); !flow.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v: ten minutes", flow.ExpiresAt, want)
	}
	h.assertNoSecrets(flow.Verifier)

	// The cookie holds the state, for this browser only.
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != "__Host-framy_oauth_openrouter" || c.Value != state || c.Path != "/" || c.Domain != "" ||
		!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 600 {
		t.Errorf("cookie = %+v", c)
	}
	// A callback carries a code in its query; neither route may be cached or
	// leak its address in a Referer.
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rr.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if got := metric(t, "framy_oauth_started_total", ""); got != before+1 {
		t.Errorf("started counter moved by %d", got-before)
	}
}

func TestEachStartMakesNewSecrets(t *testing.T) {
	h := newHarness(t)
	a, b := h.browser(ann), h.browser(bob)
	first, second := a.start(), b.start()
	qa, qb := mustQuery(t, first), mustQuery(t, second)
	if qa.Get("state") == qb.Get("state") || qa.Get("code_challenge") == qb.Get("code_challenge") {
		t.Fatal("two starts shared a state or a challenge")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestStartForNobodyGoesToSignInAndBack(t *testing.T) {
	h := newHarness(t)
	rr := h.browser(0).get(StartPath)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/auth/login?return_to=%2Foauth%2Fopenrouter%2Fstart" {
		t.Fatalf("status %d, Location %q", rr.Code, rr.Header().Get("Location"))
	}
	if h.store.count() != 0 || len(rr.Result().Cookies()) != 0 {
		t.Error("a flow or a cookie was made for nobody")
	}
	if auths, _, _ := h.or.counts(); auths != 0 {
		t.Error("OpenRouter was reached")
	}
}

func TestOnlyGetStartsOrEndsAFlow(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{StartPath, CallbackPath} {
			if rr := b.request(method, path); rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d, want 405", method, path, rr.Code)
			}
		}
	}
	if h.store.count() != 0 {
		t.Error("a flow was made by a request that was not a GET")
	}
}

func TestStartThatCannotKeepTheFlowEndsWithAResult(t *testing.T) {
	h := newHarness(t)
	h.store.failPut = true
	b := h.browser(ann)
	rr := b.get(StartPath)
	if got := h.resultOf(rr); got != Unavailable {
		t.Fatalf("result %q", got)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Error("a cookie was set for a flow that was not kept")
	}
}

func TestWholeFlowSavesTheKeyForThePerson(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)

	authURL := b.start()
	state := mustQuery(t, authURL).Get("state")
	verifier := h.store.peek(t, state).Verifier
	target := b.approve(authURL)
	_, code := callbackParams(t, target)

	rr := b.get(target)
	if got := h.resultOf(rr); got != Connected {
		t.Fatalf("result %q; log:\n%s", got, h.logs.String())
	}
	if rr.Header().Get("Location") != "/?connect=openrouter&result=connected" {
		t.Errorf("Location = %q", rr.Header().Get("Location"))
	}
	key := h.or.lastKey()
	if got := h.keys.saved(); len(got) != 1 || got[0] != (save{ann, "openrouter", key}) {
		t.Fatalf("saved %+v, want the minted key for the person", got)
	}
	if h.store.count() != 0 {
		t.Error("the flow is still there")
	}
	if _, ok := b.jar[cookieName]; ok {
		t.Error("the cookie was not cleared")
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if _, exchanges, _ := h.or.counts(); exchanges != 1 {
		t.Errorf("%d exchanges", exchanges)
	}
	h.assertNoSecrets(code, verifier, key)
	if strings.Contains(h.logs.String(), state) {
		t.Error("the log holds the state")
	}
}

func TestSecondCallbackWithTheSameStateIsRefused(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	authURL := b.start()
	target := b.approve(authURL)
	cookie := b.jar[cookieName]

	if got := b.callback(target); got != Connected {
		t.Fatalf("first: %q", got)
	}
	// The same browser, with the cookie it would still have had, and the
	// same URL: a replayed callback.
	b.jar[cookieName] = cookie
	if got := b.callback(target); got != Expired {
		t.Fatalf("replay: %q, want %q", got, Expired)
	}
	if len(h.keys.saved()) != 1 {
		t.Errorf("saved %d keys", len(h.keys.saved()))
	}
	if _, exchanges, _ := h.or.counts(); exchanges != 1 {
		t.Errorf("%d exchanges: the replay reached OpenRouter", exchanges)
	}
}

// refusal runs a flow up to the callback and lets the test change what the
// callback meets. It checks the common outcome of a refusal: nothing saved,
// the flow spent, the cookie gone, OpenRouter's exchange not asked, nothing
// secret said.
func refusal(t *testing.T, want Result, arrange func(h *harness, b *browser, target string) (*browser, string)) {
	t.Helper()
	h := newHarness(t)
	b := h.browser(ann)
	authURL := b.start()
	state := mustQuery(t, authURL).Get("state")
	verifier := h.store.peek(t, state).Verifier
	target := b.approve(authURL)
	_, code := callbackParams(t, target)

	use, target := arrange(h, b, target)
	if got := use.callback(target); got != want {
		t.Fatalf("result %q, want %q", got, want)
	}
	if got := h.keys.saved(); len(got) != 0 {
		t.Errorf("saved %+v", got)
	}
	if _, exchanges, _ := h.or.counts(); exchanges != 0 {
		t.Errorf("OpenRouter was asked to exchange %d times", exchanges)
	}
	if _, ok := use.jar[cookieName]; ok {
		t.Error("the cookie was not cleared")
	}
	h.assertNoSecrets(code, verifier)
}

func withState(t *testing.T, target, state string) string {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if state == "" {
		q.Del("state")
	} else {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

func TestAWrongStateIsRefused(t *testing.T) {
	stranger, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	for name, wrong := range map[string]func(right string) string{
		"another state": func(string) string { return stranger },
		"no state":      func(string) string { return "" },
		"the last character": func(right string) string {
			return right[:len(right)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(right, "A")]
		},
		"cut short": func(right string) string { return right[:len(right)-1] },
		"with more": func(right string) string { return right + "x" },
		"its hash": func(right string) string {
			sum := hashState(right)
			return base64.RawURLEncoding.EncodeToString(sum[:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			refusal(t, Expired, func(h *harness, b *browser, target string) (*browser, string) {
				right, _ := callbackParams(t, target)
				return b, withState(t, target, wrong(right))
			})
		})
	}
}

func TestAStateWithoutTheCookieIsRefused(t *testing.T) {
	// The callback URL opened in a browser that never started the flow: a
	// victim led to an attacker's callback, or the cookie gone with a
	// cleared jar.
	refusal(t, Expired, func(h *harness, b *browser, target string) (*browser, string) {
		return h.browser(ann), target
	})
}

func TestACookieOfAnUnknownStateIsRefused(t *testing.T) {
	refusal(t, Expired, func(h *harness, b *browser, target string) (*browser, string) {
		stranger := h.browser(ann)
		forged, _ := randomToken()
		stranger.jar[cookieName] = forged
		return stranger, target
	})
}

func TestTheStateOfAnotherPersonIsRefused(t *testing.T) {
	// Ann's flow, finished in a browser where Bob is now signed in: the
	// account was switched in the middle. Neither of them gets the key.
	refusal(t, Expired, func(h *harness, b *browser, target string) (*browser, string) {
		b.user = bob
		return b, target
	})
}

func TestACallbackWithNobodySignedInIsRefused(t *testing.T) {
	refusal(t, SignedOut, func(h *harness, b *browser, target string) (*browser, string) {
		b.user = 0
		return b, target
	})
}

func TestAnAttackersCallbackOpenedByTheVictimIsRefused(t *testing.T) {
	// Ann starts a flow for her own account and approves it. Bob is lured
	// into opening her callback URL, with the cookie of a flow of his own or
	// with none. The key would be Ann's, but Bob's account must not take it
	// in, and Bob's flow must not be used to take Ann's code.
	for name, bobStarts := range map[string]bool{"bob has a flow of his own": true, "bob has no flow": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			attacker := h.browser(ann)
			target := attacker.approve(attacker.start())

			victim := h.browser(bob)
			if bobStarts {
				victim.start()
			}
			if got := victim.callback(target); got != Expired {
				t.Fatalf("result %q", got)
			}
			if got := h.keys.saved(); len(got) != 0 {
				t.Errorf("saved %+v", got)
			}
			if _, exchanges, _ := h.or.counts(); exchanges != 0 {
				t.Error("the attacker's code was exchanged")
			}
			// Ann's own flow is untouched: she can still finish it.
			if got := attacker.callback(target); got != Connected {
				t.Errorf("the attacker's own browser: %q", got)
			}
		})
	}
}

func TestAFlowExpiresAfterTenMinutes(t *testing.T) {
	for name, tc := range map[string]struct {
		wait time.Duration
		want Result
	}{
		"a second before":   {10*time.Minute - time.Second, Connected},
		"at ten minutes":    {10 * time.Minute, Expired},
		"long after":        {time.Hour, Expired},
		"a second to spare": {time.Second, Connected},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			b := h.browser(ann)
			target := b.approve(b.start())
			h.clock.Advance(tc.wait)
			if got := b.callback(target); got != tc.want {
				t.Fatalf("after %v: %q, want %q", tc.wait, got, tc.want)
			}
			wantSaved := 0
			if tc.want == Connected {
				wantSaved = 1
			}
			if got := len(h.keys.saved()); got != wantSaved {
				t.Errorf("saved %d keys, want %d", got, wantSaved)
			}
			if _, exchanges, _ := h.or.counts(); exchanges != wantSaved {
				t.Errorf("%d exchanges, want %d", exchanges, wantSaved)
			}
		})
	}
}

func TestStartingAgainReplacesTheEarlierFlow(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	first := b.approve(b.start())
	// The cookie of the first start is overwritten by the second.
	second := b.approve(b.start())
	if h.store.count() != 1 {
		t.Fatalf("%d flows for one person", h.store.count())
	}
	if got := b.callback(first); got != Expired {
		t.Errorf("the replaced flow: %q", got)
	}
	// The mismatch spent the new flow too: a stale tab costs a restart, and
	// never a key. Starting once more works.
	if got := b.callback(second); got != Expired {
		t.Errorf("the second flow after the first was refused: %q", got)
	}
	if got, _ := b.connect(); got != Connected {
		t.Errorf("a fresh start: %q", got)
	}
}

func TestACancelledAuthorizationEndsWithoutAnExchange(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		want  Result
	}{
		"access denied":      {"error=access_denied&state=%s", Cancelled},
		"no code":            {"state=%s", Cancelled},
		"an empty code":      {"code=&state=%s", Cancelled},
		"another error":      {"error=server_error&error_description=a+secret+words&state=%s", Refused},
		"a code too long":    {"code=" + strings.Repeat("c", 2000) + "&state=%s", Refused},
		"error beats a code": {"error=access_denied&code=or-code-x&state=%s", Cancelled},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			b := h.browser(ann)
			state := mustQuery(t, b.start()).Get("state")
			if got := b.callback(CallbackPath + "?" + fmt.Sprintf(tc.query, url.QueryEscape(state))); got != tc.want {
				t.Fatalf("result %q, want %q", got, tc.want)
			}
			if _, exchanges, _ := h.or.counts(); exchanges != 0 {
				t.Error("OpenRouter was asked to exchange")
			}
			if h.store.count() != 0 || len(h.keys.saved()) != 0 {
				t.Error("the flow outlived the callback, or a key was saved")
			}
			h.assertNoSecrets("a+secret+words", "a secret words")
		})
	}
}

func TestAFailedExchangeSavesNothingAndSaysSo(t *testing.T) {
	for name, tc := range map[string]struct {
		force int
		body  string
		want  Result
	}{
		"an invalid code":     {http.StatusBadRequest, "", Refused},
		"forbidden":           {http.StatusForbidden, "", Refused},
		"method not allowed":  {http.StatusMethodNotAllowed, "", Refused},
		"unauthorized":        {http.StatusUnauthorized, "", Refused},
		"too many requests":   {http.StatusTooManyRequests, "", Unavailable},
		"a server error":      {http.StatusInternalServerError, "", Unavailable},
		"a gateway timeout":   {http.StatusGatewayTimeout, "", Unavailable},
		"a redirect":          {http.StatusTemporaryRedirect, "", Unavailable},
		"an empty answer":     {http.StatusOK, "{}", Unavailable},
		"an answer, no key":   {http.StatusOK, `{"key":"","user_id":"u"}`, Unavailable},
		"an answer, not JSON": {http.StatusOK, "<html>maintenance</html>", Unavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("the exchange followed a redirect to %s", r.URL)
			}))
			defer evil.Close()
			h.or.set(func(f *fakeOpenRouter) {
				f.force = tc.force
				f.body = tc.body
			})
			h.or.set(func(f *fakeOpenRouter) { f.redirect = evil.URL })
			b := h.browser(ann)
			authURL := b.start()
			state := mustQuery(t, authURL).Get("state")
			verifier := h.store.peek(t, state).Verifier
			target := b.approve(authURL)
			_, code := callbackParams(t, target)

			if got := b.callback(target); got != tc.want {
				t.Fatalf("result %q, want %q", got, tc.want)
			}
			if got := h.keys.saved(); len(got) != 0 {
				t.Errorf("saved %+v", got)
			}
			if h.store.count() != 0 {
				t.Error("the flow was kept for a retry")
			}
			if _, ok := b.jar[cookieName]; ok {
				t.Error("the cookie was not cleared")
			}
			// The provider's refusal quotes the code and the verifier back.
			h.assertNoSecrets(code, verifier)
		})
	}
}

func TestAnExchangeThatDoesNotAnswerSavesNothing(t *testing.T) {
	t.Run("a stalled answer", func(t *testing.T) {
		h := newHarness(t)
		h.or.set(func(f *fakeOpenRouter) { f.delay = 400 * time.Millisecond })
		h.newConnector(Config{HTTP: &http.Client{Timeout: 50 * time.Millisecond}})
		b := h.browser(ann)
		if got, _ := b.connect(); got != Unavailable {
			t.Fatalf("result %q", got)
		}
		if len(h.keys.saved()) != 0 {
			t.Error("a key was saved")
		}
	})
	t.Run("a server that is down", func(t *testing.T) {
		h := newHarness(t)
		b := h.browser(ann)
		target := b.approve(b.start())
		h.or.Close()
		if got := b.callback(target); got != Unavailable {
			t.Fatalf("result %q", got)
		}
		if len(h.keys.saved()) != 0 || h.store.count() != 0 {
			t.Error("a key was saved or the flow kept")
		}
	})
}

func TestAKeyThatCannotBeSavedEndsWithAResult(t *testing.T) {
	const leaky = "sk-or-v1-leakedinanerror"
	for name, tc := range map[string]struct {
		err  error
		want Result
	}{
		"the key is rejected":    {fmt.Errorf("%w: %s", keys.ErrRejected, leaky), Refused},
		"the key is forbidden":   {keys.ErrForbidden, Refused},
		"the key is malformed":   {keys.ErrBadKey, Refused},
		"the check was not made": {fmt.Errorf("%w: OpenRouter answered 503", keys.ErrUnavailable), Unavailable},
		"framy keeps no keys":    {keys.ErrDisabled, Unavailable},
		"the database is down":   {fmt.Errorf("save the connection: %s", leaky), Unavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.keys.err = tc.err
			b := h.browser(ann)
			if got, _ := b.connect(); got != tc.want {
				t.Fatalf("result %q, want %q", got, tc.want)
			}
			if h.store.count() != 0 {
				t.Error("the flow was kept")
			}
			// The exchange did give a key; whatever is said about it holds
			// no part of it, nor of an error that quoted one.
			h.assertNoSecrets(h.or.lastKey(), leaky)
		})
	}
}

func TestConcurrentCallbacksSpendTheFlowOnce(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	target := b.approve(b.start())
	cookie := b.jar[cookieName]

	const n = 24
	answers := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			racer := h.browser(ann)
			racer.jar[cookieName] = cookie
			answers[i] = racer.get(target)
		}()
	}
	wg.Wait()
	counts := map[Result]int{}
	for _, rr := range answers {
		counts[h.resultOf(rr)]++
	}
	if counts[Connected] != 1 || counts[Expired] != n-1 {
		t.Fatalf("results %v, want one connected and %d expired", counts, n-1)
	}
	if len(h.keys.saved()) != 1 {
		t.Errorf("saved %d keys", len(h.keys.saved()))
	}
	if _, exchanges, _ := h.or.counts(); exchanges != 1 {
		t.Errorf("%d exchanges", exchanges)
	}
}

func TestCallbackResultsAreCounted(t *testing.T) {
	h := newHarness(t)
	before := map[Result]int64{}
	for _, r := range []Result{Connected, Expired, Cancelled} {
		before[r] = metric(t, "framy_oauth_callbacks_total", string(r))
	}
	b := h.browser(ann)
	b.connect()
	b.callback(CallbackPath + "?code=x&state=y") // no cookie any more
	state := mustQuery(t, b.start()).Get("state")
	b.callback(CallbackPath + "?error=access_denied&state=" + url.QueryEscape(state))
	for r, want := range map[Result]int64{Connected: 1, Expired: 1, Cancelled: 1} {
		if got := metric(t, "framy_oauth_callbacks_total", string(r)) - before[r]; got != want {
			t.Errorf("%s: counted %d, want %d", r, got, want)
		}
	}
}

func TestNewChecksItsConfig(t *testing.T) {
	h := newHarness(t)
	ok := Config{Store: h.store, Keys: h.keys, Person: person, SiteURL: "https://framy.example"}
	if _, err := New(ok); err != nil {
		t.Fatalf("a good config: %v", err)
	}
	local := ok
	local.SiteURL = "http://localhost:8080"
	if _, err := New(local); err != nil {
		t.Fatalf("localhost: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no store":                    func(c *Config) { c.Store = nil },
		"no saver":                    func(c *Config) { c.Keys = nil },
		"nobody to say who is asking": func(c *Config) { c.Person = nil },
		"no site":                     func(c *Config) { c.SiteURL = "" },
		"a site over http":            func(c *Config) { c.SiteURL = "http://framy.example" },
		"a site with a path":          func(c *Config) { c.SiteURL = "https://framy.example/app" },
		"a site with a query":         func(c *Config) { c.SiteURL = "https://framy.example?x=1" },
		"a lookalike of localhost":    func(c *Config) { c.SiteURL = "http://localhost.evil.example" },
		"a login path off the site":   func(c *Config) { c.LoginPath = "//evil.example/login" },
		"a login path with a query":   func(c *Config) { c.LoginPath = "/login?x=1" },
		"a settings path off site":    func(c *Config) { c.Settings = "https://evil.example/" },
		"a settings path with a \\":   func(c *Config) { c.Settings = `/\evil.example` },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := ok
			mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestTheSettingsAndLoginPathsCanBeChanged(t *testing.T) {
	h := newHarness(t)
	conn, err := New(Config{
		Store: h.store, Keys: h.keys, Person: person, SiteURL: siteOrigin,
		AuthURL: h.or.URL + "/auth", APIBase: h.or.URL + "/api/v1", Now: h.clock.Now,
		LoginPath: "/login", Settings: "/settings/keys",
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	conn.Start(rr, httptest.NewRequest(http.MethodGet, StartPath, nil))
	if got := rr.Header().Get("Location"); got != "/login?return_to=%2Foauth%2Fopenrouter%2Fstart" {
		t.Errorf("login redirect %q", got)
	}
	rr = httptest.NewRecorder()
	conn.Callback(rr, httptest.NewRequest(http.MethodGet, CallbackPath, nil))
	if got := rr.Header().Get("Location"); got != "/settings/keys?connect=openrouter&result=expired" {
		t.Errorf("settings redirect %q", got)
	}
}

// A callback address with a long or odd query must not break anything.
func TestOddCallbacksAreRefusedNotCrashed(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	state := mustQuery(t, b.start()).Get("state")
	for _, q := range []string{"", "?", "?state=", "?code=%zz&state=" + state, "?state=" + strings.Repeat("s", 100_000)} {
		b.jar[cookieName] = state
		got := b.callback(CallbackPath + q)
		if got == Connected {
			t.Errorf("%q connected", q)
		}
		// Each callback spends the flow; make one again for the next query.
		state = mustQuery(t, b.start()).Get("state")
	}
}
