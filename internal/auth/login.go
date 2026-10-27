// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// loginCookie holds what the callback needs to recognise the browser that
// started the sign-in: the state, the nonce, the PKCE verifier and where to go
// afterwards. It lives ten minutes and is cleared by the callback, whatever
// the outcome. A table would need a write for every visitor of /auth/login,
// signed in or not, and a sweep; the cookie needs neither, and what it holds
// is of use only to the browser it was set on.
const (
	loginCookie = "__Host-framy_login"
	loginLife   = 10 * time.Minute
)

// scopes of the sign-in: `profile` gives the `id` claim, which is the
// account, and `telegram:bot_access` lets the bot write to the person's DM,
// so a result made on the site can arrive in Telegram.
var scopes = []string{oidc.ScopeOpenID, "profile", "telegram:bot_access"}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r,omitempty"`
	At       int64  `json:"t"`
}

// provider is what the first sign-in reads from the discovery document.
type provider struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// provider reads Telegram's discovery document once, on the first sign-in,
// and keeps the result. A failure is not kept: the next attempt asks again.
// Starting without the network is not an error of this package's.
func (a *Auth) provider(ctx context.Context) (*provider, error) {
	a.provMu.Lock()
	defer a.provMu.Unlock()
	if a.prov != nil {
		return a.prov, nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, a.http), a.issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", a.issuer, err)
	}
	ep := p.Endpoint()
	// Said outright: auto-detection would try the body first and, on a
	// refusal, the header, which is a second request carrying the secret.
	ep.AuthStyle = oauth2.AuthStyleInHeader
	a.prov = &provider{
		oauth: &oauth2.Config{
			ClientID:     a.clientID,
			ClientSecret: a.clientSecret,
			Endpoint:     ep,
			RedirectURL:  a.redirectURL,
			Scopes:       scopes,
		},
		// The discovery document also lists ES256K, which this verifier
		// would not take anyway; the list is cut to what Telegram signs
		// with. The context only carries the HTTP client for the key set.
		verifier: p.VerifierContext(oidc.ClientContext(context.Background(), a.http), &oidc.Config{
			ClientID:             a.clientID,
			SupportedSigningAlgs: []string{oidc.RS256},
			Now:                  a.now,
		}),
	}
	return a.prov, nil
}

// Login starts Telegram Login: it makes a state, a nonce and a PKCE verifier,
// keeps them in a short-lived cookie and redirects to Telegram. `return_to`
// is where the person lands afterwards; only a path on this site is believed.
func (a *Auth) Login() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.SiteEnabled() {
			signInUnavailable(w)
			return
		}
		p, err := a.provider(r.Context())
		if err != nil {
			a.log.Warn("sign-in provider unavailable", "error", err)
			metrics.AuthResults.Inc("site_unavailable")
			signInUnavailable(w)
			return
		}
		state, err1 := randomString()
		nonce, err2 := randomString()
		if err := errors.Join(err1, err2); err != nil {
			a.log.Error("random for sign-in", "error", err)
			signInUnavailable(w)
			return
		}
		st := loginState{
			State:    state,
			Nonce:    nonce,
			Verifier: oauth2.GenerateVerifier(),
			ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")),
			At:       a.now().Unix(),
		}
		raw, _ := json.Marshal(st)
		http.SetCookie(w, &http.Cookie{
			Name:     loginCookie,
			Value:    base64.RawURLEncoding.EncodeToString(raw),
			Path:     "/",
			MaxAge:   int(loginLife.Seconds()),
			HttpOnly: true,
			Secure:   true,
			// The callback is a top-level GET from Telegram's site, which
			// Lax sends and Strict would not.
			SameSite: http.SameSiteLaxMode,
		})
		target := p.oauth.AuthCodeURL(st.State,
			oauth2.S256ChallengeOption(st.Verifier),
			oidc.Nonce(st.Nonce))
		redirect(w, target)
	})
}

// failure is a refused sign-in: reason is what the person's browser is told,
// result is the metric and the log line.
type failure struct {
	reason, result string
	err            error
}

// Callback is Telegram's redirect back. It checks the state against the
// cookie, trades the code (with the PKCE verifier, the client authenticated
// in a header) for tokens, verifies the id_token and its nonce, takes the
// account from the `id` claim, records the person in core and starts a
// session. A failure redirects to `/?sign_in=<reason>` with reason one of
// `cancelled`, `expired` and `failed`, and sets no session.
func (a *Auth) Callback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.SiteEnabled() {
			signInUnavailable(w)
			return
		}
		st, haveState := a.readLogin(r)
		clearCookie(w, loginCookie) // used once, whatever happens
		var (
			userID int64
			f      *failure
		)
		if haveState {
			userID, f = a.finishLogin(r, st)
		} else {
			f = &failure{reason: "expired", result: "site_no_state"}
		}
		if f != nil {
			metrics.AuthResults.Inc(f.result)
			level := slog.LevelWarn
			if f.result == "site_no_state" || f.result == "site_denied" {
				level = slog.LevelInfo
			}
			a.log.Log(r.Context(), level, "sign-in refused", "result", f.result, "error", f.err)
			redirect(w, "/?sign_in="+f.reason)
			return
		}
		secret, expires, err := a.newSession(r.Context(), userID, KindSite, SessionLife)
		if err != nil {
			a.log.Error("start a session", "error", err)
			metrics.AuthResults.Inc("site_error")
			redirect(w, "/?sign_in=failed")
			return
		}
		a.setSessionCookie(w, secret, expires)
		metrics.AuthResults.Inc("site_ok")
		redirect(w, safeReturnTo(st.ReturnTo))
	})
}

// finishLogin is the callback after the cookie was found: every check the
// design lists, in the order that spends the least before refusing.
func (a *Auth) finishLogin(r *http.Request, st loginState) (int64, *failure) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return 0, &failure{reason: "cancelled", result: "site_denied"}
		}
		return 0, &failure{reason: "failed", result: "site_provider", err: fmt.Errorf("the provider answered error=%q", e)}
	}
	code := q.Get("code")
	if code == "" || !equal(q.Get("state"), st.State) {
		return 0, &failure{reason: "failed", result: "site_state", err: errors.New("state does not match")}
	}
	p, err := a.provider(r.Context())
	if err != nil {
		return 0, &failure{reason: "failed", result: "site_unavailable", err: err}
	}
	ctx := oidc.ClientContext(r.Context(), a.http)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		// An oauth2.RetrieveError carries the provider's body; its error
		// code is all that is kept.
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			err = fmt.Errorf("the token endpoint refused the code: %s %s", re.Response.Status, re.ErrorCode)
		}
		return 0, &failure{reason: "failed", result: "site_exchange", err: err}
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return 0, &failure{reason: "failed", result: "site_token", err: errors.New("no id_token in the token response")}
	}
	idToken, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return 0, &failure{reason: "failed", result: "site_token", err: err}
	}
	// Verify does not check the nonce; this does.
	if st.Nonce == "" || !equal(idToken.Nonce, st.Nonce) {
		return 0, &failure{reason: "failed", result: "site_nonce", err: errors.New("nonce does not match")}
	}
	var c claims
	if err := idToken.Claims(&c); err != nil {
		return 0, &failure{reason: "failed", result: "site_claims", err: err}
	}
	user, err := c.user()
	if err != nil {
		return 0, &failure{reason: "failed", result: "site_claims", err: err}
	}
	if err := a.people.Touch(r.Context(), user, nil); err != nil {
		return 0, &failure{reason: "failed", result: "site_error", err: fmt.Errorf("record the person in core: %w", err)}
	}
	return user.ID, nil
}

// claims are the parts of Telegram's id_token this package reads. `sub` is
// not among them: the account is `id`, the Telegram user id the bot and the
// Mini App use, and `sub` is not that number.
type claims struct {
	ID                json.RawMessage `json:"id"`
	Name              string          `json:"name"`
	GivenName         string          `json:"given_name"`
	FamilyName        string          `json:"family_name"`
	PreferredUsername string          `json:"preferred_username"`
	Locale            string          `json:"locale"`
}

// user builds the tg.User that core.touch takes. `id` is a number in
// Telegram's tokens; a string of digits is read as well, and anything else
// (a fraction, zero, a negative, nothing) is no account.
func (c claims) user() (tg.User, error) {
	idText := strings.TrimSpace(string(c.ID))
	if s, err := strconv.Unquote(idText); err == nil {
		idText = s
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return tg.User{}, errors.New("the id_token has no usable id claim")
	}
	u := tg.User{ID: id, Username: c.PreferredUsername, LanguageCode: language(c.Locale)}
	switch {
	case c.GivenName != "" || c.FamilyName != "":
		u.FirstName, u.LastName = c.GivenName, c.FamilyName
	case c.Name != "" && !strings.ContainsAny(c.Name, " \t\n"):
		// A name of one word is a first name. A longer one cannot be told
		// from "Mary Ann" and "Smith" apart, and a wrong split would
		// overwrite what the bot wrote to the shared core.person; it is left
		// out, and the bot fills the names in at the person's next message.
		u.FirstName = c.Name
	}
	return u, nil
}

// language reduces a locale such as "en-US" or "ru_RU" to the two or three
// letters core.touch takes, or "" for anything else.
func language(locale string) string {
	l, _, _ := strings.Cut(strings.ReplaceAll(locale, "_", "-"), "-")
	l = strings.ToLower(l)
	if len(l) < 2 || len(l) > 3 {
		return ""
	}
	for _, r := range l {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return l
}

// readLogin reads the login cookie. A cookie that is missing, damaged or
// older than loginLife is no cookie.
func (a *Auth) readLogin(r *http.Request) (loginState, bool) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return loginState{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return loginState{}, false
	}
	var st loginState
	if err := json.Unmarshal(raw, &st); err != nil || st.State == "" || st.Verifier == "" {
		return loginState{}, false
	}
	age := a.now().Sub(time.Unix(st.At, 0))
	if age > loginLife || age < -time.Minute {
		return loginState{}, false
	}
	return st, true
}

// safeReturnTo returns the path if it is one on this site and "/" otherwise.
// A browser takes `//host` and `/\host` for another host, and ignores tabs
// and line breaks inside a URL, so a path is accepted only when it starts
// with a single slash and holds nothing but visible ASCII and no backslash.
// A path into /auth is refused as well, so a sign-in cannot send itself round
// again.
func safeReturnTo(s string) string {
	if s == "" || len(s) > 512 || s[0] != '/' || (len(s) > 1 && s[1] == '/') {
		return "/"
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7f || c == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	if p := u.Path; p == "/auth" || strings.HasPrefix(p, "/auth/") {
		return "/"
	}
	return s
}

// randomString is 256 random bits as base64url.
func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// redirect sends the browser on with 303, so a POST is never repeated.
func redirect(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
}

func signInUnavailable(w http.ResponseWriter) {
	http.Error(w, "sign-in is not available", http.StatusServiceUnavailable)
}
