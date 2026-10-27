// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stand-in for oauth.telegram.org: the discovery document, the keys, the
// authorization endpoint that approves at once, and a token endpoint that is
// as strict as the real one is meant to be (the client authenticates in a
// header, the PKCE verifier has to match the challenge, a code is good once).

var (
	keyOnce  sync.Once
	theKey   *rsa.PrivateKey
	otherKey *rsa.PrivateKey
)

// keys makes the two RSA keys once: the one the fake publishes and one it
// does not.
func keys(t *testing.T) (published, unpublished *rsa.PrivateKey) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if theKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if otherKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return theKey, otherKey
}

const (
	testClientID     = "123456789"
	testClientSecret = "client-secret-DO-NOT-LOG-7f3a"
	testSiteURL      = "https://framy.test"
	testBotToken     = "7000001:AAtest-bot-token-DO-NOT-LOG"
	testUserID       = int64(4242)
)

type issuedCode struct {
	challenge, nonce, redirect string
	used                       bool
}

type tokenRequest struct {
	basicOK       bool // the client id and secret came in the Authorization header
	bodyHasSecret bool // ... or, wrongly, in the body
	form          url.Values
}

type fakeIDP struct {
	*httptest.Server
	t     *testing.T
	clock *testClock

	mu            sync.Mutex
	codes         map[string]*issuedCode
	authRequests  []url.Values
	tokenRequests []tokenRequest
	idTokens      []string // every id_token it issued

	// What the next sign-ins get. A test changes these before it signs in.
	userID   any    // the id claim: an int64 by default
	sub      string // never the account
	name     string
	username string
	// claims edits the claims before they are signed.
	claims func(map[string]any)
	// issue replaces the signing: it gets the claims and returns the token.
	issue func(map[string]any) string
	// deny makes the authorization endpoint refuse, as a person who pressed
	// Cancel would.
	deny bool
	// discovery is the status of the discovery document; 0 is 200.
	discovery int
	// rejectTokens makes the next token requests answer 401 invalid_client,
	// as a provider does that dislikes how the client authenticated.
	rejectTokens int
}

func newFakeIDP(t *testing.T, clock *testClock) *fakeIDP {
	t.Helper()
	f := &fakeIDP{
		t: t, clock: clock, codes: map[string]*issuedCode{},
		userID: testUserID, sub: "sub-opaque-0e9c1a", name: "Olesya", username: "olesya_u",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.serveDiscovery)
	mux.HandleFunc("GET /.well-known/jwks.json", f.serveKeys)
	mux.HandleFunc("GET /auth", f.serveAuthorize)
	mux.HandleFunc("POST /token", f.serveToken)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeIDP) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	status := f.discovery
	f.mu.Unlock()
	if status != 0 {
		http.Error(w, "down", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                   f.URL,
		"authorization_endpoint":   f.URL + "/auth",
		"token_endpoint":           f.URL + "/token",
		"jwks_uri":                 f.URL + "/.well-known/jwks.json",
		"response_types_supported": []string{"code"},
		"subject_types_supported":  []string{"public"},
		// Telegram lists ES256K as well. RS512 is here so that the tests
		// prove the verifier's own restriction to RS256, not the provider's
		// list: a verifier that believed this document would take it.
		"id_token_signing_alg_values_supported": []string{"RS256", "RS512", "ES256", "EdDSA", "ES256K"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"scopes_supported":                      []string{"openid", "profile", "phone", "telegram:bot_access"},
	})
}

func (f *fakeIDP) serveKeys(w http.ResponseWriter, r *http.Request) {
	k, _ := keys(f.t)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test-key",
		"n": b64(k.N.Bytes()),
		"e": b64(big.NewInt(int64(k.E)).Bytes()),
	}}})
}

func (f *fakeIDP) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authRequests = append(f.authRequests, q)
	redirect := q.Get("redirect_uri")
	if q.Get("client_id") != testClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		redirect != testSiteURL+"/auth/callback" ||
		!strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	back, _ := url.Parse(redirect)
	out := back.Query()
	out.Set("state", q.Get("state"))
	if f.deny {
		out.Set("error", "access_denied")
	} else {
		code := randHex()
		f.codes[code] = &issuedCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: redirect}
		out.Set("code", code)
	}
	back.RawQuery = out.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakeIDP) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, hasBasic := r.BasicAuth()
	// RFC 6749 2.3.1: both are form-urlencoded before they are joined.
	unid, _ := url.QueryUnescape(id)
	unsecret, _ := url.QueryUnescape(secret)
	req := tokenRequest{
		basicOK:       hasBasic && unid == testClientID && unsecret == testClientSecret,
		bodyHasSecret: r.PostForm.Has("client_secret"),
		form:          r.PostForm,
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenRequests = append(f.tokenRequests, req)
	refuse := func(why string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, why)
	}
	if f.rejectTokens > 0 {
		f.rejectTokens--
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	c := f.codes[r.PostForm.Get("code")]
	switch {
	case !req.basicOK:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	case r.PostForm.Get("grant_type") != "authorization_code" || c == nil || c.used:
		refuse("unknown or used code")
		return
	case r.PostForm.Get("redirect_uri") != c.redirect:
		refuse("redirect_uri")
		return
	}
	c.used = true
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if b64(sum[:]) != c.challenge {
		refuse("PKCE verifier does not match")
		return
	}

	now := f.clock.Now()
	claims := map[string]any{
		"iss": f.URL, "aud": testClientID, "sub": f.sub, "id": f.userID,
		"name": f.name, "preferred_username": f.username,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": c.nonce,
	}
	if f.claims != nil {
		f.claims(claims)
	}
	var token string
	if f.issue != nil {
		token = f.issue(claims)
	} else {
		token = signRS256(f.t, claims)
	}
	f.idTokens = append(f.idTokens, token)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-token-DO-NOT-LOG", "token_type": "Bearer", "expires_in": 3600, "id_token": token,
	})
}

func (f *fakeIDP) lastAuthRequest() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authRequests) == 0 {
		f.t.Fatal("the provider saw no authorization request")
	}
	return f.authRequests[len(f.authRequests)-1]
}

func (f *fakeIDP) tokenRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokenRequests)
}

func (f *fakeIDP) lastIDToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.idTokens) == 0 {
		f.t.Fatal("the provider issued no id_token")
	}
	return f.idTokens[len(f.idTokens)-1]
}

// JWTs, built by hand so that a test can make the broken ones too.

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randHex() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}

// jwt joins a header and claims and signs the result with sign.
func jwt(header, claims map[string]any, sign func(input string) []byte) string {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	return input + "." + b64(sign(input))
}

func rsaSigner(key *rsa.PrivateKey, h crypto.Hash) func(string) []byte {
	return func(input string) []byte {
		var sum []byte
		switch h {
		case crypto.SHA256:
			s := sha256.Sum256([]byte(input))
			sum = s[:]
		case crypto.SHA512:
			s := sha512.Sum512([]byte(input))
			sum = s[:]
		}
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, h, sum)
		if err != nil {
			panic(err)
		}
		return sig
	}
}

// signRS256 is the token Telegram would send: RS256, the published key.
func signRS256(t *testing.T, claims map[string]any) string {
	k, _ := keys(t)
	return jwt(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"}, claims, rsaSigner(k, crypto.SHA256))
}

// signWith signs RS256 with a key the provider does not publish.
func signWithOtherKey(t *testing.T, claims map[string]any) string {
	_, k := keys(t)
	return jwt(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"}, claims, rsaSigner(k, crypto.SHA256))
}

// signRS512 uses the published key but an algorithm the verifier does not take.
func signRS512(t *testing.T, claims map[string]any) string {
	k, _ := keys(t)
	return jwt(map[string]any{"alg": "RS512", "typ": "JWT", "kid": "test-key"}, claims, rsaSigner(k, crypto.SHA512))
}

// signHS256WithPublicKey is the old confusion attack: the public key, which
// everyone has, used as an HMAC secret.
func signHS256WithPublicKey(t *testing.T, claims map[string]any) string {
	k, _ := keys(t)
	return jwt(map[string]any{"alg": "HS256", "typ": "JWT", "kid": "test-key"}, claims, func(input string) []byte {
		m := hmac.New(sha256.New, k.N.Bytes())
		m.Write([]byte(input))
		return m.Sum(nil)
	})
}

// signNone is an unsigned token.
func signNone(claims map[string]any) string {
	return jwt(map[string]any{"alg": "none", "typ": "JWT"}, claims, func(string) []byte { return nil })
}

// editPayload changes a signed token's claims and keeps its signature.
func editPayload(token string, edit func(map[string]any)) string {
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	edit(c)
	out, _ := json.Marshal(c)
	parts[1] = b64(out)
	return strings.Join(parts, ".")
}
