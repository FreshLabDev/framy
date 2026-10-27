// SPDX-License-Identifier: Apache-2.0

// Package auth says who is asking (docs/design.md, Accounts). One account is
// one Telegram user id, and there are two ways in:
//
//   - The site signs a person in with Telegram Login, which is OpenID Connect
//     at oauth.telegram.org: GET /auth/login sends the browser there, GET
//     /auth/callback checks what comes back and starts a session. The session
//     is a random value in a `__Host-framy_session` cookie; the database holds
//     only its SHA-256. Telegram issues no refresh token, so a session lasts
//     SessionLife and then the person signs in again.
//   - The Mini App posts its initData to POST /auth/miniapp, which checks the
//     HMAC and answers a bearer token that lasts MiniAppTokenLife. Cookies in
//     the Telegram Web iframe are unreliable, so the Mini App carries
//     `Authorization: Bearer <token>`.
//
// Other code asks two things of an *Auth: Person for who a request is from,
// and Require to refuse a handler's requests from nobody. PersonKey is
// Person in the form web.Config.Person wants, so the per-person rate limits
// count the signed-in person.
//
// Sign-in on the site needs the Telegram Login client (a client id and
// secret from BotFather) and SiteURL; without them /auth/login and
// /auth/callback answer 503 and everything else runs. The Mini App needs
// only the bot token.
package auth

import (
	"context"
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
	// SessionLife is how long a site session lasts, counted from sign-in. It
	// is fixed, not extended by use: the cookie's Max-Age is set once, and
	// Telegram would make the person sign in again with one tap.
	SessionLife = 30 * 24 * time.Hour
	// MiniAppTokenLife is how long a Mini App's bearer token lasts. It matches
	// MaxInitDataAge: the same initData cannot be exchanged again after an
	// hour, so a Mini App left open longer has to be opened again.
	MiniAppTokenLife = time.Hour
	// DefaultIssuer is Telegram's OpenID Connect provider.
	DefaultIssuer = "https://oauth.telegram.org"

	// cacheTTL is how long a found session is believed without asking the
	// database again. Logout and "sign out everywhere" drop entries at once,
	// so it matters only for rows deleted behind this process's back.
	cacheTTL = 30 * time.Second
	// maxCached bounds the cache. Only live sessions are cached, so it takes
	// that many signed-in people to reach it; it is then emptied.
	maxCached = 10_000
	// sweepEvery is how often a new session removes the expired ones.
	sweepEvery = time.Hour
	// sweepGrace keeps an expired row a day before it goes, for whoever looks
	// at why a person was signed out.
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

	provMu sync.Mutex
	prov   *provider

	// cache holds sessions found lately, by token hash. gen counts the
	// revocations: a lookup that began before one is not cached.
	cacheMu sync.Mutex
	cache   map[[32]byte]cached
	gen     uint64

	sweepMu sync.Mutex
	sweepAt time.Time
}

type cached struct {
	user  int64
	kind  Kind
	until time.Time
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
func (a *Auth) Mount(site *web.Server) {
	site.Handle("GET /auth/login", a.Login())
	site.Handle("GET /auth/callback", a.Callback())
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
	now := a.now()
	a.cacheMu.Lock()
	if e, ok := a.cache[hash]; ok {
		if e.kind == kind && now.Before(e.until) {
			a.cacheMu.Unlock()
			return e.user, true, nil
		}
		delete(a.cache, hash)
	}
	gen := a.gen
	a.cacheMu.Unlock()

	s, found, err := a.sessions.Lookup(ctx, hash, kind, now)
	if err != nil || !found {
		return 0, false, err
	}
	a.cacheMu.Lock()
	if a.gen == gen {
		if len(a.cache) >= maxCached {
			clear(a.cache)
		}
		a.cache[hash] = cached{user: s.UserID, kind: kind, until: minTime(s.Expires, now.Add(cacheTTL))}
	}
	a.cacheMu.Unlock()
	return s.UserID, true, nil
}

// forget drops one session from the cache; forgetUser drops a person's.
func (a *Auth) forget(hash [32]byte) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	a.gen++
	delete(a.cache, hash)
}

func (a *Auth) forgetUser(userID int64) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	a.gen++
	for h, e := range a.cache {
		if e.user == userID {
			delete(a.cache, h)
		}
	}
}

// newSession stores a session for the person and returns the secret that
// opens it, which is the only copy there will ever be.
func (a *Auth) newSession(ctx context.Context, userID int64, kind Kind, life time.Duration) (secret string, expires time.Time, err error) {
	secret, hash, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := a.now()
	expires = now.Add(life)
	if err := a.sessions.Create(ctx, Session{Hash: hash, UserID: userID, Kind: kind, Created: now, Expires: expires}); err != nil {
		return "", time.Time{}, err
	}
	a.sweep(ctx, now)
	return secret, expires, nil
}

// sweep removes expired sessions, at most once an hour and only when someone
// signs in, so there is no goroutine to stop. A row that has expired is never
// accepted, so a failed sweep costs nothing.
func (a *Auth) sweep(ctx context.Context, now time.Time) {
	a.sweepMu.Lock()
	due := !now.Before(a.sweepAt)
	if due {
		a.sweepAt = now.Add(sweepEvery)
	}
	a.sweepMu.Unlock()
	if !due {
		return
	}
	if _, err := a.sessions.DeleteExpired(ctx, now.Add(-sweepGrace)); err != nil {
		a.log.Warn("sweep expired sessions", "error", err)
	}
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
