// SPDX-License-Identifier: Apache-2.0

// Package auth says who is asking (docs/design.md, Accounts). One account is
// one Telegram user id, and there are two ways in:
//
//   - The site signs a person in with Telegram Login, which is OpenID Connect
//     at oauth.telegram.org: GET /auth/login sends the browser there, GET
//     /auth/callback checks what comes back and starts a session. The session
//     is a random value in a `__Host-framy_session` cookie; the database holds
//     only its SHA-256. Telegram issues no refresh token, so framy keeps the
//     session going itself: it lasts SessionLife from the last time the site
//     loaded the person (Renew), and then the person signs in again.
//   - The Mini App posts its initData to POST /auth/miniapp, which checks the
//     HMAC and answers a bearer token that ends with the initData, at most
//     MiniAppTokenLife from now. One launch gets one token: it is kept while
//     it lasts and given again to the same initData. Cookies in the Telegram
//     Web iframe are unreliable, so the Mini App carries
//     `Authorization: Bearer <token>`.
//
// Other code asks two things of an *Auth: Person for who a request is from,
// and Require to refuse a handler's requests from nobody. PersonKey is
// Person in the form web.Config.Person wants, so the per-person rate limits
// count the signed-in person.
//
// Sign-in on the site needs the Telegram Login client (a client id and
// secret from BotFather) and SiteURL; without them /auth/login and
// /auth/callback send the browser back to the site with
// ?sign_in=unavailable and everything else runs. The Mini App needs
// only the bot token.
package auth

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/config"
	"github.com/FreshLabDev/framy/internal/web"
)

const (
	// SessionCookie is the cookie of a signed-in browser. The `__Host-` prefix
	// makes a browser refuse it unless it is Secure, has Path=/ and no Domain,
	// so no other host of the domain can set one for the site.
	SessionCookie = "__Host-framy_session"
	// SessionLife is how long a site session lasts after it was last used:
	// Renew moves its end, in the database and in the cookie's Max-Age, when
	// the site loads the person. Someone who comes back within a month is
	// never signed out in the middle of their work; a session nobody uses
	// ends. The settings say so, counted from the last visit
	// (web.prefs.signed_sub, TestTheSiteSaysHowLongASignInLasts).
	SessionLife = 30 * 24 * time.Hour
	// renewEvery is how stale a session's end may get before Renew moves it:
	// at most one write per session a day, however often the site loads.
	renewEvery = 24 * time.Hour
	// MiniAppTokenLife is how long a Mini App's bearer token lasts at most.
	// It ends with the initData it was exchanged for, MaxInitDataAge after
	// Telegram signed it, however late the exchange: one launch is good for
	// an hour in all, and a Mini App left open longer has to be opened
	// again.
	MiniAppTokenLife = time.Hour
	// minTokenLife is the least a token is worth making for: initData with
	// less of its hour left is answered as too old, which tells the person
	// to open the Mini App again at once instead of after one more refusal.
	minTokenLife = time.Minute
	// DefaultIssuer is Telegram's OpenID Connect provider.
	DefaultIssuer = "https://oauth.telegram.org"

	// cacheTTL is how long a found session is believed without asking the
	// database again. Logout and "sign out everywhere" drop entries at once,
	// in this process and, through SessionsChannel, in every other one, so it
	// matters only for a notice that did not come (the listener was
	// reconnecting) and rows deleted otherwise (the person left core).
	cacheTTL = 30 * time.Second
	// maxCached bounds the cache. Only live sessions are cached, so it takes
	// that many signed-in people to reach it; it is then emptied.
	maxCached = 10_000
	// sweepGrace keeps an expired row a day before Sweep removes it, for
	// whoever looks at why a person was signed out.
	sweepGrace = 24 * time.Hour
)

// People records a person in core before anything references them, and
// keeps the name and photo the site shows of them. *db.Store is one.
type People interface {
	Touch(ctx context.Context, user tg.User, chat *tg.Chat) error
	// SetCard writes over the name and photo of the person's last sign-in.
	SetCard(ctx context.Context, userID int64, name, photoURL string) error
}

// Config is what Auth needs. The Telegram Login client is optional; the
// rest is not.
type Config struct {
	// ClientID and ClientSecret are the Telegram Login client from BotFather.
	// Both or neither.
	ClientID, ClientSecret string
	// SiteURL is the site's public origin, https://framy.ws04.cc, with no path.
	// The redirect URI is SiteURL + "/auth/callback", and it has to be
	// registered with the client. http is accepted for localhost only.
	SiteURL string
	// Issuer is the OpenID Connect issuer; DefaultIssuer when empty. Tests
	// point it at a local provider.
	Issuer string
	// BotToken derives the key that signs a Mini App's initData.
	BotToken string

	Sessions Sessions
	People   People

	// HTTP makes the calls to the provider (discovery, keys, the token
	// endpoint). A client with a 10 s timeout when nil. It is copied and
	// never follows a redirect: the client secret travels in a header.
	HTTP *http.Client
	Log  *slog.Logger
	// Now is the clock, for tests.
	Now func() time.Time
}

// Auth is the sign-in of the site and the Mini App. Its methods are safe for
// concurrent use.
type Auth struct {
	clientID, clientSecret string
	redirectURL            string
	issuer                 string
	http                   *http.Client
	sessions               Sessions
	people                 People
	initData               *Verifier
	log                    *slog.Logger
	now                    func() time.Time
	// loginKey signs the login cookie. It is derived from the client
	// secret, so it outlives a restart and stays the stack's own.
	loginKey []byte

	provMu sync.Mutex
	prov   *provider

	// cache holds sessions found lately, by token hash. gen counts the
	// revocations: a lookup that began before one is not cached.
	cacheMu sync.Mutex
	cache   map[[32]byte]cached
	gen     uint64

	// minted is the Mini App token each launch got, by its initData's
	// hash, while the token lasts: the same launch asking again gets the
	// same token instead of a new row. mintLocks keep two exchanges of one
	// launch from both making one; a launch takes the lock its hash falls
	// on.
	mintMu    sync.Mutex
	minted    map[[32]byte]mintedToken
	mintLocks [64]sync.Mutex
}

// mintedToken is a Mini App token kept for the launch it was made for.
type mintedToken struct {
	secret  string
	hash    [32]byte
	user    int64
	expires time.Time
}

type cached struct {
	user int64
	kind Kind
	// until is when the entry stops being believed; expires is when the
	// session itself ends.
	until, expires time.Time
}

// New checks the settings and builds the Auth. It makes no network call:
// Telegram's discovery document is read when the first person signs in.
func New(cfg Config) (*Auth, error) {
	switch {
	case cfg.Sessions == nil || cfg.People == nil:
		return nil, errors.New("auth: sessions and people are required")
	case cfg.BotToken == "":
		return nil, errors.New("auth: the bot token is required to check a Mini App's initData")
	case (cfg.ClientID == "") != (cfg.ClientSecret == ""):
		return nil, errors.New("auth: the Telegram Login client id and secret go together")
	}
	a := &Auth{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		issuer:       cfg.Issuer,
		sessions:     cfg.Sessions,
		people:       cfg.People,
		log:          cfg.Log,
		now:          cfg.Now,
		cache:        map[[32]byte]cached{},
		minted:       map[[32]byte]mintedToken{},
	}
	if a.issuer == "" {
		a.issuer = DefaultIssuer
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if a.now == nil {
		a.now = time.Now
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	if cfg.HTTP != nil {
		c := *cfg.HTTP
		hc = &c
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	a.http = hc
	a.initData = NewVerifier(cfg.BotToken, a.now)
	if a.clientID != "" {
		origin, err := config.ParseSiteURL(cfg.SiteURL)
		if err != nil {
			return nil, fmt.Errorf("auth: SiteURL %w", err)
		}
		a.redirectURL = origin + "/auth/callback"
		if a.loginKey, err = hkdf.Key(sha256.New, []byte(cfg.ClientSecret), nil, loginKeyInfo, sha256.Size); err != nil {
			return nil, fmt.Errorf("auth: the key of the login cookie: %w", err)
		}
	}
	return a, nil
}

// SiteEnabled reports whether the site can sign people in: the Telegram Login
// client is configured.
func (a *Auth) SiteEnabled() bool { return a.clientID != "" }

// Mount registers the sign-in routes on the public server:
//
//	GET  /auth/login        starts Telegram Login (?return_to=/path, optional)
//	GET  /auth/callback     Telegram's redirect back
//	POST /auth/logout       ends the session or token of the request
//	POST /auth/logout/all   ends every session and token of the person
//	POST /auth/miniapp      {"init_data": "..."} -> a bearer token
//
// The two a browser navigates to send it back to the site with
// ?sign_in=unavailable when they are over their rate limit, as when Telegram
// cannot be reached, so the person reads why on the page they came from.
func (a *Auth) Mount(site *web.Server) {
	site.Handle("GET /auth/login", a.Login(), web.OnLimit(unavailablePath))
	site.Handle("GET /auth/callback", a.Callback(), web.OnLimit(unavailablePath))
	site.Handle("POST /auth/logout", a.Logout())
	site.Handle("POST /auth/logout/all", a.Require(a.LogoutAll()))
	// initData is a kilobyte or two.
	site.Handle("POST /auth/miniapp", a.MiniApp(), web.Body(16<<10))
}

// setCard keeps the name and photo of a sign-in for the site to show. It is
// not worth refusing the sign-in for: a failure is logged, and the site
// shows the person without them until the next sign-in.
func (a *Auth) setCard(ctx context.Context, userID int64, name, photoURL string) {
	if err := a.people.SetCard(ctx, userID, name, photoURL); err != nil {
		a.log.Warn("keep the name and photo of a sign-in", "error", err)
	}
}

// Person is the signed-in person behind the request: the Telegram user id
// from the session cookie or from `Authorization: Bearer`. It is cheap. A
// session found is remembered for cacheTTL, so most calls are a map lookup,
// and a call that is not is one query on the primary key. A request that
// carries a bearer token is judged by it alone; a bad token does not fall back
// to the cookie. A database error counts as nobody signed in; Require tells
// the two apart.
func (a *Auth) Person(r *http.Request) (userID int64, ok bool) {
	id, ok, err := a.identify(r)
	if err != nil {
		a.log.Error("look up a session", "error", err)
		return 0, false
	}
	return id, ok
}

// Identify is Person for a caller that answers a failed lookup itself:
// (0, false, nil) is nobody signed in, an error a database that did not
// answer. The job API gives 503 for the one and 401 for the other.
func (a *Auth) Identify(r *http.Request) (userID int64, ok bool, err error) {
	return a.identify(r)
}

// PersonKey is Person for web.Config.Person: the user id as text, or "" when
// nobody is signed in.
func (a *Auth) PersonKey(r *http.Request) string {
	if id, ok := a.Person(r); ok {
		return strconv.FormatInt(id, 10)
	}
	return ""
}

type personKey struct{}

// Require answers 401 `{"error":"sign_in"}` to a request from nobody, and 503
// `{"error":"unavailable"}` when it cannot tell. Otherwise it runs h, in
// which Person is answered from the request without another lookup.
func (a *Auth) Require(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok, err := a.identify(r)
		switch {
		case err != nil:
			a.log.Error("look up a session", "error", err)
			writeError(w, http.StatusServiceUnavailable, "unavailable")
		case !ok:
			w.Header().Set("WWW-Authenticate", `Bearer realm="framy"`)
			writeError(w, http.StatusUnauthorized, "sign_in")
		default:
			h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personKey{}, id)))
		}
	})
}

// RevokeAll ends every session and token of the person, on the site and in
// the Mini App, and says how many there were. It is what "sign out
// everywhere" and deleting an account call.
func (a *Auth) RevokeAll(ctx context.Context, userID int64) (int64, error) {
	n, err := a.sessions.DeleteUser(ctx, userID)
	a.forgetUser(userID)
	return n, err
}

// identify finds the person of a request. (0, false, nil) is nobody; an
// error is a database that did not answer.
func (a *Auth) identify(r *http.Request) (int64, bool, error) {
	if id, ok := r.Context().Value(personKey{}).(int64); ok {
		return id, true, nil
	}
	kind, hash, ok := credential(r)
	if !ok {
		return 0, false, nil
	}
	return a.lookup(r.Context(), kind, hash)
}

// credential reads what the request signs in with. A bearer token is looked
// for first, and if there is one the cookie is not looked at.
func credential(r *http.Request) (Kind, [32]byte, bool) {
	if tok, present := bearer(r); present {
		h, ok := hashToken(tok)
		return KindMiniApp, h, ok
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		h, ok := hashToken(c.Value)
		return KindSite, h, ok
	}
	return "", [32]byte{}, false
}

// bearer returns the token of `Authorization: Bearer <token>`. present is
// true for any Bearer header, even one with no usable token.
func bearer(r *http.Request) (token string, present bool) {
	h := r.Header.Get("Authorization")
	scheme, rest, found := strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func (a *Auth) lookup(ctx context.Context, kind Kind, hash [32]byte) (int64, bool, error) {
	e, ok, err := a.find(ctx, kind, hash)
	return e.user, ok, err
}

// find is lookup with the session's end.
func (a *Auth) find(ctx context.Context, kind Kind, hash [32]byte) (cached, bool, error) {
	now := a.now()
	a.cacheMu.Lock()
	if e, ok := a.cache[hash]; ok {
		if e.kind == kind && now.Before(e.until) {
			a.cacheMu.Unlock()
			return e, true, nil
		}
		delete(a.cache, hash)
	}
	gen := a.gen
	a.cacheMu.Unlock()

	s, found, err := a.sessions.Lookup(ctx, hash, kind, now)
	if err != nil || !found {
		return cached{}, false, err
	}
	e := cached{user: s.UserID, kind: kind, until: minTime(s.Expires, now.Add(cacheTTL)), expires: s.Expires}
	a.cacheMu.Lock()
	if a.gen == gen {
		if len(a.cache) >= maxCached {
			clear(a.cache)
		}
		a.cache[hash] = e
	}
	a.cacheMu.Unlock()
	return e, true, nil
}

// Renew keeps the site session of the request going while it is used. The
// site calls it when it loads the person (GET /api/me), at every visit and
// on coming back to the tab: a session renewed more than renewEvery ago
// lasts SessionLife from now, in the database and in the cookie, whose
// Max-Age is set again. A bearer token is not renewed; the Mini App's ends
// with the initData it came from. A failure is only logged: the session
// still lasts until its old end.
//
// A renewal also tells core the person was here, as a sign-in does: with
// the session kept going, the site may see no sign-in for months, and the
// family's presence would stop at the last one. Only the id goes, so the
// names and the language core holds stay as they are.
func (a *Auth) Renew(w http.ResponseWriter, r *http.Request) {
	if _, present := bearer(r); present {
		return
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return
	}
	hash, ok := hashToken(c.Value)
	if !ok {
		return
	}
	e, ok, err := a.find(r.Context(), KindSite, hash)
	if err != nil || !ok {
		return
	}
	now := a.now()
	if e.expires.Sub(now) > SessionLife-renewEvery {
		return
	}
	expires := now.Add(SessionLife)
	found, err := a.sessions.Extend(r.Context(), hash, expires, now)
	if err != nil {
		a.log.Warn("renew a session", "error", err)
		return
	}
	if !found {
		// Ended meanwhile: logged out in another tab, or everywhere.
		a.forget(hash)
		return
	}
	a.cacheMu.Lock()
	if e, ok := a.cache[hash]; ok {
		e.expires = expires
		a.cache[hash] = e
	}
	a.cacheMu.Unlock()
	a.setSessionCookie(w, c.Value, expires)
	if err := a.people.Touch(r.Context(), tg.User{ID: e.user}, nil); err != nil {
		a.log.Warn("record the person in core at a renewal", "error", err)
	}
}

// ForgetAll drops every session this process remembers, so each is looked
// up in the database again on its next request. framy calls it when another
// process ended sessions (SessionsChannel), and when its listener connects,
// since a notice may have gone by meanwhile.
func (a *Auth) ForgetAll() {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	a.gen++
	clear(a.cache)
}

// forget drops one session from the cache; forgetUser drops a person's.
// A Mini App token kept for its launch goes with its session, so the launch
// asking again gets a new one, and a token that ended stays ended.
func (a *Auth) forget(hash [32]byte) {
	a.cacheMu.Lock()
	a.gen++
	delete(a.cache, hash)
	a.cacheMu.Unlock()
	a.unmint(func(t mintedToken) bool { return t.hash == hash })
}

func (a *Auth) forgetUser(userID int64) {
	a.cacheMu.Lock()
	a.gen++
	for h, e := range a.cache {
		if e.user == userID {
			delete(a.cache, h)
		}
	}
	a.cacheMu.Unlock()
	a.unmint(func(t mintedToken) bool { return t.user == userID })
}

// mintedFor returns the token kept for a Mini App launch, if it is still
// worth giving out: its session is live and has at least minTokenLife left.
// One that is not is dropped.
func (a *Auth) mintedFor(ctx context.Context, launch [32]byte, userID int64) (mintedToken, bool) {
	a.mintMu.Lock()
	t, ok := a.minted[launch]
	a.mintMu.Unlock()
	if !ok {
		return mintedToken{}, false
	}
	if t.user == userID && t.expires.Sub(a.now()) >= minTokenLife {
		if e, live, err := a.find(ctx, KindMiniApp, t.hash); err == nil && live && e.user == userID {
			return t, true
		}
	}
	a.mintMu.Lock()
	if a.minted[launch] == t {
		delete(a.minted, launch)
	}
	a.mintMu.Unlock()
	return mintedToken{}, false
}

// mint keeps the token made for a Mini App launch. The map holds a token
// per launch of the last hour; past maxCached the ended ones go, and if that
// is not enough, all of them, which costs a row per launch asking again.
func (a *Auth) mint(launch [32]byte, t mintedToken) {
	now := a.now()
	a.mintMu.Lock()
	defer a.mintMu.Unlock()
	if len(a.minted) >= maxCached {
		for k, m := range a.minted {
			if !now.Before(m.expires) {
				delete(a.minted, k)
			}
		}
		if len(a.minted) >= maxCached {
			clear(a.minted)
		}
	}
	a.minted[launch] = t
}

func (a *Auth) unmint(match func(mintedToken) bool) {
	a.mintMu.Lock()
	defer a.mintMu.Unlock()
	for k, t := range a.minted {
		if match(t) {
			delete(a.minted, k)
		}
	}
}

// newSession stores a session for the person that ends at expires and
// returns the secret that opens it, with its hash. The site's secret lives
// only in the cookie; a Mini App's is also kept for its launch while it
// lasts (mint).
func (a *Auth) newSession(ctx context.Context, userID int64, kind Kind, expires time.Time) (secret string, hash [32]byte, err error) {
	secret, hash, err = newToken()
	if err != nil {
		return "", hash, err
	}
	if err := a.sessions.Create(ctx, Session{Hash: hash, UserID: userID, Kind: kind, Created: a.now(), Expires: expires}); err != nil {
		return "", hash, err
	}
	return secret, hash, nil
}

// Sweep removes the sessions and tokens that expired more than sweepGrace
// ago, and says how many. A row that has expired is never accepted, so this
// only keeps the table small; the service calls it at start and every hour
// on the stack that keeps the shared tables.
func (a *Auth) Sweep(ctx context.Context) (int64, error) {
	return a.sessions.DeleteExpired(ctx, a.now().Add(-sweepGrace))
}

// newToken makes a secret of 256 random bits and its hash. The secret is
// base64url, 43 characters. A hash of a value that random needs no salt and
// no slow function: there is nothing to guess.
func newToken() (secret string, hash [32]byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", hash, fmt.Errorf("auth: random: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(b[:])
	return secret, sha256.Sum256([]byte(secret)), nil
}

// tokenLen is the length of a secret made by newToken.
const tokenLen = 43

// hashToken hashes a secret a client sent, after checking that it has the
// shape of one, so garbage costs no query.
func hashToken(secret string) ([32]byte, bool) {
	if len(secret) != tokenLen {
		return [32]byte{}, false
	}
	if b, err := base64.RawURLEncoding.DecodeString(secret); err != nil || len(b) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(secret)), true
}

func (a *Auth) setSessionCookie(w http.ResponseWriter, secret string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    secret,
		Path:     "/",
		MaxAge:   max(1, int(expires.Sub(a.now()).Seconds())),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Logout ends the session of the cookie and the token of the Authorization
// header, whichever the request has, clears the cookie and answers 204. A
// request with neither is answered 204 too: the person is signed out either
// way.
func (a *Auth) Logout() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hashes [][32]byte
		if tok, present := bearer(r); present {
			if h, ok := hashToken(tok); ok {
				hashes = append(hashes, h)
			}
		}
		if c, err := r.Cookie(SessionCookie); err == nil {
			if h, ok := hashToken(c.Value); ok {
				hashes = append(hashes, h)
			}
		}
		for _, h := range hashes {
			a.forget(h)
			if err := a.sessions.Delete(r.Context(), h); err != nil {
				// Keep the cookie: the session is still alive, and the
				// person has to try again.
				a.log.Error("end a session", "error", err)
				writeError(w, http.StatusServiceUnavailable, "unavailable")
				return
			}
		}
		clearCookie(w, SessionCookie)
		w.WriteHeader(http.StatusNoContent)
	})
}

// LogoutAll ends every session and token of the signed-in person. Wrap it in
// Require, as Mount does.
func (a *Auth) LogoutAll() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Person(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign_in")
			return
		}
		if _, err := a.RevokeAll(r.Context(), id); err != nil {
			a.log.Error("end every session of a person", "error", err)
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		clearCookie(w, SessionCookie)
		w.WriteHeader(http.StatusNoContent)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
