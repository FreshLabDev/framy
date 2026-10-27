// SPDX-License-Identifier: Apache-2.0

// Package oauth connects a person's OpenRouter account to framy with OAuth
// PKCE (docs/design.md, Providers and keys). The person never sees or pastes
// a key: OpenRouter makes one for framy and framy stores it as it stores a
// pasted one (keys.Service.Save).
//
// Two routes carry the flow, both on the public listener:
//
//	GET /oauth/openrouter/start     (Connector.Start)
//	GET /oauth/openrouter/callback  (Connector.Callback)
//
// Start, for a signed-in person, makes a random verifier and state, keeps the
// verifier and the hash of the state for ten minutes (Store), puts the state
// in a __Host- cookie of the person's browser and redirects to OpenRouter
// with the S256 challenge of the verifier. A person nobody knows is sent to
// the sign-in page and back.
//
// Callback is where OpenRouter sends the browser back, with a code. Nothing
// is exchanged until all of these hold:
//
//   - the browser has the cookie, and the state in the URL is the cookie's;
//   - the store has the flow of the cookie's state, and has not yet spent it:
//     the callback spends it before it checks anything else, so no callback
//     can use a state twice, and a wrong one burns the flow it hit;
//   - the flow has not expired;
//   - the person signed in now is the person the flow was started for.
//
// Then the code goes to OpenRouter with the verifier, and the key that comes
// back goes through keys.Service.Save, which checks it and encrypts it. The
// browser is sent on to the site, always, with a result the frontend reads
// (Result); a key, a code or a verifier is in no URL, log line or error.
//
// Four things tie the end of a flow to its start, and none stands in for
// another. PKCE: the code is useless without this server's verifier, so a code
// made for someone else's challenge exchanges for nothing. The person: a flow
// cannot be finished in the session of another account. The cookie: only the
// browser that started the flow can finish it, so a state that leaked in a URL
// or a log, or a second browser of the same person, finishes nothing. The
// single use: whatever a callback finds wrong, it has spent the flow, so there
// is nothing left to retry or guess at.
package oauth

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// Provider is the one provider that connects by OAuth: its name in keys and
// in the store.
const Provider = catalog.OpenRouter

// The routes, for the code that registers them on the web server.
const (
	StartPath    = "/oauth/openrouter/start"
	CallbackPath = "/oauth/openrouter/callback"
)

// FlowLifetime is how long a flow lasts from its start: the time to log in at
// OpenRouter and press Authorize. OpenRouter's own code lives as long.
const FlowLifetime = 10 * time.Minute

// cookieName: the __Host- prefix makes a browser take the cookie only if it
// is Secure, has Path=/ and no Domain, so another subdomain cannot set it.
const cookieName = "__Host-framy_oauth_openrouter"

// keyLabel is the name OpenRouter shows for the key in the person's list, so
// they know which one to revoke.
const keyLabel = "framy"

// The defaults of Config.
const (
	DefaultAuthURL   = "https://openrouter.ai/auth"
	DefaultAPIBase   = "https://openrouter.ai/api/v1"
	DefaultLoginPath = "/auth/login"
	DefaultSettings  = "/"
)

// Result is how a flow ended, as the frontend reads it from the site's URL
// after the callback: /?connect=openrouter&result=<Result>. The callback
// never answers with a page of its own.
type Result string

const (
	// Connected: the key is saved. A key the person had before, pasted or
	// from an earlier connect, is replaced; it is not revoked at OpenRouter,
	// which only the person can do, at openrouter.ai/settings/keys.
	Connected Result = "connected"
	// Cancelled: the person did not authorize at OpenRouter.
	Cancelled Result = "cancelled"
	// Expired: there is no flow this browser can finish: the start was more
	// than ten minutes ago, was replaced by a newer one, was already used,
	// belongs to another person or another browser. Start again.
	Expired Result = "expired"
	// SignedOut: nobody is signed in on the callback. Sign in, then start
	// again.
	SignedOut Result = "signin"
	// Refused: OpenRouter, or the check of the key, refused what it was
	// given. Starting again may help; the same result twice is for the owner.
	Refused Result = "refused"
	// Unavailable: OpenRouter or framy could not be reached or failed. Try
	// again later. Nothing was saved.
	Unavailable Result = "unavailable"
)

// Saver is the part of keys.Service the callback uses.
type Saver interface {
	Save(ctx context.Context, userID int64, provider, key string) (keys.Connection, error)
}

// Config is what the Connector needs.
type Config struct {
	Store Store
	Keys  Saver
	// Person names the signed-in person behind a request. Sign-in provides
	// it; it has to answer false for a request nobody is signed in on.
	Person func(*http.Request) (userID int64, ok bool)
	// SiteURL is the site's origin, https://framy.example (SITE_URL). The
	// callback URL given to OpenRouter is SiteURL + CallbackPath. It must be
	// https, or http on a loopback host for work on a laptop, which
	// OpenRouter takes too.
	SiteURL string
	// AuthURL is OpenRouter's page the person is sent to, APIBase the root of
	// its API; their defaults when empty. Tests point them at a stand-in.
	AuthURL, APIBase string
	// LoginPath is where a person nobody knows is sent to sign in, with
	// ?return_to=<the start path>. Settings is the site page every callback
	// ends on, "/" until the settings page exists. Both are paths of this
	// site.
	LoginPath, Settings string
	// HTTP makes the exchange; a client with a 10 s timeout when nil. It is
	// copied and never follows a redirect.
	HTTP      *http.Client
	UserAgent string
	Log       *slog.Logger
	// Now is the clock of the flow's lifetime, for tests.
	Now func() time.Time
}

// Connector serves the two routes.
type Connector struct {
	store       Store
	keys        Saver
	person      func(*http.Request) (int64, bool)
	authURL     string
	apiBase     string
	callbackURL string
	loginPath   string
	settings    string
	http        *http.Client
	userAgent   string
	log         *slog.Logger
	now         func() time.Time
}

// callbackTimeout bounds a whole callback: the exchange and the check of the
// key, each of which has a timeout of its own.
const callbackTimeout = 30 * time.Second

// New checks cfg and builds the Connector.
func New(cfg Config) (*Connector, error) {
	if cfg.Store == nil || cfg.Keys == nil || cfg.Person == nil {
		return nil, errors.New("oauth: a store, a key saver and a way to tell who is asking are required")
	}
	site, err := url.Parse(cfg.SiteURL)
	if err != nil || site.Host == "" || site.User != nil || site.RawQuery != "" || site.Fragment != "" ||
		(site.Path != "" && site.Path != "/") {
		return nil, errors.New("oauth: SITE_URL must be an origin such as https://framy.example")
	}
	if site.Scheme != "https" && !(site.Scheme == "http" && isLoopback(site.Hostname())) {
		return nil, errors.New("oauth: SITE_URL must be https, or http on localhost: OpenRouter takes no other callback")
	}
	c := &Connector{
		store:       cfg.Store,
		keys:        cfg.Keys,
		person:      cfg.Person,
		authURL:     orDefault(cfg.AuthURL, DefaultAuthURL),
		apiBase:     strings.TrimRight(orDefault(cfg.APIBase, DefaultAPIBase), "/"),
		callbackURL: site.Scheme + "://" + site.Host + CallbackPath,
		loginPath:   orDefault(cfg.LoginPath, DefaultLoginPath),
		settings:    orDefault(cfg.Settings, DefaultSettings),
		userAgent:   cfg.UserAgent,
		log:         cfg.Log,
		now:         cfg.Now,
	}
	for _, p := range []string{c.loginPath, c.settings} {
		// A path of this site, never "//host" or "/\host", which a browser
		// would take for another one.
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, `\?#`) {
			return nil, fmt.Errorf("oauth: %q is not a path of this site", p)
		}
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	if cfg.HTTP != nil {
		copied := *cfg.HTTP
		hc = &copied
	}
	// The code and the verifier are in the request body: they must not
	// follow a redirect to another host.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = hc
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Start sends the signed-in person to OpenRouter. It is a GET because the
// Mini App opens it with openLink and sign-in returns to it with a redirect,
// and neither can post. That is safe: all it does is open a flow for the
// person who is already signed in, bound to their own browser by a cookie, so
// a page that sends someone here gets them an OpenRouter consent screen and
// nothing else. A flow opened this way replaces the person's earlier one,
// which is the most a stranger can spoil.
func (c *Connector) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := c.person(r)
	if !ok {
		http.Redirect(w, r, c.loginPath+"?return_to="+url.QueryEscape(StartPath), http.StatusSeeOther)
		return
	}
	state, err := randomToken()
	if err != nil {
		c.startFailed(w, r, err)
		return
	}
	verifier, err := randomToken()
	if err != nil {
		c.startFailed(w, r, err)
		return
	}
	now := c.now()
	err = c.store.Put(r.Context(), Flow{
		State:     hashState(state),
		UserID:    user,
		Provider:  Provider,
		Verifier:  verifier,
		ExpiresAt: now.Add(FlowLifetime),
	}, now)
	if err != nil {
		c.startFailed(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: state, Path: "/", MaxAge: int(FlowLifetime / time.Second),
		HttpOnly: true, Secure: true,
		// Lax, not Strict: the callback is a top-level navigation from
		// openrouter.ai, which Lax sends the cookie on and Strict does not.
		SameSite: http.SameSiteLaxMode,
	})
	metrics.OAuthStarted.Inc()
	http.Redirect(w, r, c.authRedirect(state, challenge(verifier)), http.StatusSeeOther)
}

func (c *Connector) startFailed(w http.ResponseWriter, r *http.Request, err error) {
	c.log.Error("cannot start the OpenRouter connect", "error", err)
	c.finish(w, r, Unavailable)
}

// authRedirect is OpenRouter's page for this flow.
func (c *Connector) authRedirect(state, challenge string) string {
	q := url.Values{}
	q.Set("callback_url", c.callbackURL)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("key_label", keyLabel)
	return c.authURL + "?" + q.Encode()
}

// Callback ends a flow. Whatever happens, the cookie is cleared and the
// browser is sent to the site with a Result; the details of a refusal go to
// the log without anything secret in them.
func (c *Connector) Callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), callbackTimeout)
	defer cancel()
	result, reason := c.complete(ctx, r)
	metrics.OAuthCallbacks.Inc(string(result))
	level := slog.LevelWarn
	if result == Connected || result == Cancelled {
		level = slog.LevelInfo
	}
	c.log.Log(ctx, level, "OpenRouter connect ended", "result", result, "reason", reason)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	c.finish(w, r, result)
}

// finish sends the browser to the site with the result.
func (c *Connector) finish(w http.ResponseWriter, r *http.Request, result Result) {
	q := url.Values{"connect": {Provider}, "result": {string(result)}}
	http.Redirect(w, r, c.settings+"?"+q.Encode(), http.StatusSeeOther)
}

// complete runs the callback's checks in order and returns how it ended with
// a fixed reason for the log. Nothing in either comes from the request.
func (c *Connector) complete(ctx context.Context, r *http.Request) (Result, string) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return Expired, "no cookie"
	}
	// Spent first: from here on this flow is over, whatever else is wrong.
	flow, err := c.store.Take(ctx, hashState(cookie.Value))
	switch {
	case errors.Is(err, ErrNoFlow):
		return Expired, "no such flow"
	case err != nil:
		c.log.Error("cannot take the OAuth flow", "error", err)
		return Unavailable, "store"
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(cookie.Value)) != 1 {
		return Expired, "state differs from the cookie"
	}
	if !c.now().Before(flow.ExpiresAt) {
		return Expired, "flow expired"
	}
	user, ok := c.person(r)
	switch {
	case !ok:
		return SignedOut, "nobody is signed in"
	case user != flow.UserID:
		return Expired, "flow of another person"
	case flow.Provider != Provider:
		return Expired, "flow of another provider"
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return Cancelled, "access denied"
		}
		return Refused, "provider error"
	}
	code := q.Get("code")
	switch {
	case code == "":
		return Cancelled, "no code"
	case len(code) > maxCodeLen:
		return Refused, "code too long"
	}
	key, result, reason := c.exchange(ctx, code, flow.Verifier)
	if key == "" {
		return result, reason
	}
	if _, err := c.keys.Save(ctx, flow.UserID, Provider, key); err != nil {
		return saveResult(err)
	}
	return Connected, "saved"
}

// maxCodeLen bounds what is sent on as a code; OpenRouter's are far shorter.
const maxCodeLen = 1024

// saveResult says how a refused Save ends the flow. The errors of keys carry
// no key, and neither does what is logged here.
func saveResult(err error) (Result, string) {
	switch {
	case errors.Is(err, keys.ErrRejected), errors.Is(err, keys.ErrForbidden), errors.Is(err, keys.ErrBadKey):
		return Refused, "OpenRouter refused the key it made"
	case errors.Is(err, keys.ErrDisabled):
		return Unavailable, "framy keeps no keys"
	default:
		return Unavailable, "the key could not be saved"
	}
}

// maxExchangeBody bounds what is read of OpenRouter's answer.
const maxExchangeBody = 64 << 10

// exchange trades the code and the verifier for a key at POST
// /api/v1/auth/keys. It returns the key, or an empty key with how the flow
// ends instead. Neither the answer's body nor the request's is ever logged or
// returned in an error: a provider's refusal may quote them back.
func (c *Connector) exchange(ctx context.Context, code, verifier string) (key string, result Result, reason string) {
	body, err := json.Marshal(map[string]string{
		"code": code, "code_verifier": verifier, "code_challenge_method": "S256",
	})
	if err != nil {
		return "", Unavailable, "build the request"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/auth/keys", bytes.NewReader(body))
	if err != nil {
		return "", Unavailable, "build the request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", Unavailable, "timed out"
		}
		// The cause only: the URL of a url.Error is not secret, but it is
		// not worth carrying either.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		c.log.Warn("OpenRouter did not answer the exchange", "error", err)
		return "", Unavailable, "no answer"
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxExchangeBody))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
		return "", Unavailable, fmt.Sprintf("OpenRouter answered %d", resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// A used, expired or mismatched code (400), or a refusal (403).
		return "", Refused, fmt.Sprintf("OpenRouter answered %d", resp.StatusCode)
	default:
		return "", Unavailable, fmt.Sprintf("OpenRouter answered %d", resp.StatusCode)
	}
	var answer struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.Key == "" {
		return "", Unavailable, "no key in the answer"
	}
	return answer.Key, "", ""
}
