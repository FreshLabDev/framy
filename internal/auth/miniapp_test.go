// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"
)

// signInitData builds initData the way Telegram does, for a bot token. It is
// written out apart from the code under test.
func signInitData(botToken string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	values := url.Values{}
	for k, v := range fields {
		keys = append(keys, k)
		values.Set(k, v)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + fields[k]
	}
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(botToken))
	sig := hmac.New(sha256.New, key.Sum(nil))
	sig.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(sig.Sum(nil)))
	return values.Encode()
}

// miniAppFields are the fields of a Mini App opened from the menu button.
func miniAppFields(authDate time.Time, userID int64) map[string]string {
	return map[string]string{
		"auth_date":     strconv.FormatInt(authDate.Unix(), 10),
		"query_id":      "AAHdF6IQAAAAAN0XohDhrOrc",
		"user":          `{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Olesya","last_name":"K","username":"olesya_u","language_code":"uk"}`,
		"chat_type":     "sender",
		"chat_instance": "-77",
		// Telegram signs this field with its own key for third parties, and it
		// is part of the data-check-string all the same.
		"signature": "c2lnbmVkLWJ5LXRlbGVncmFt",
	}
}

// mini posts the initData and, when it expects a token, returns it and puts it
// in the browser as the bearer token.
func (h *harness) mini(b *browser, raw string, wantStatus int) string {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"init_data": raw})
	r := b.do(http.MethodPost, "/auth/miniapp", string(body))
	if r.code != wantStatus {
		h.t.Fatalf("POST /auth/miniapp: %d %q, want %d", r.code, r.body, wantStatus)
	}
	if r.code != http.StatusOK {
		return ""
	}
	var out struct {
		Token     string `json:"token"`
		TokenType string `json:"token_type"`
		ExpiresIn int    `json:"expires_in"`
		UserID    int64  `json:"user_id"`
	}
	mustJSON(h.t, r.body, &out)
	if out.TokenType != "Bearer" || out.ExpiresIn != 3600 || out.Token == "" {
		h.t.Fatalf("answer = %+v", out)
	}
	b.bearer = out.Token
	return out.Token
}

func TestVerifyInitData(t *testing.T) {
	now := time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)
	v := NewVerifier(testBotToken, func() time.Time { return now })
	good := signInitData(testBotToken, miniAppFields(now.Add(-10*time.Minute), 42))

	got, err := v.Verify(good)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != (tg.User{ID: 42, FirstName: "Olesya", LastName: "K", Username: "olesya_u", LanguageCode: "uk"}) ||
		!got.AuthDate.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("got %+v", got)
	}

	values, _ := url.ParseQuery(good)
	with := func(edit func(url.Values)) string {
		v2 := url.Values{}
		for k, vs := range values {
			v2[k] = append([]string(nil), vs...)
		}
		edit(v2)
		return v2.Encode()
	}
	for name, tc := range map[string]struct {
		raw  string
		want error
	}{
		"another bot's token":    {signInitData("7000002:other-bot", miniAppFields(now, 42)), ErrBadSignature},
		"the user edited":        {with(func(q url.Values) { q.Set("user", `{"id":43,"first_name":"Someone"}`) }), ErrBadSignature},
		"auth_date edited":       {with(func(q url.Values) { q.Set("auth_date", strconv.FormatInt(now.Unix(), 10)) }), ErrBadSignature},
		"a field added":          {with(func(q url.Values) { q.Set("start_param", "x") }), ErrBadSignature},
		"a field dropped":        {with(func(q url.Values) { q.Del("chat_instance") }), ErrBadSignature},
		"no hash":                {with(func(q url.Values) { q.Del("hash") }), ErrBadSignature},
		"short hash":             {url.Values{"auth_date": {"1"}, "hash": {"abcd"}}.Encode(), ErrBadSignature},
		"hash not hex":           {with(func(q url.Values) { q.Set("hash", strings.Repeat("zz", 32)) }), ErrBadSignature},
		"the hash of nothing":    {with(func(q url.Values) { q.Set("hash", strings.Repeat("00", 32)) }), ErrBadSignature},
		"a field repeated":       {good + "&user=" + url.QueryEscape(`{"id":43}`), ErrMalformed},
		"not a query string":     {"%zz", ErrMalformed},
		"signed, but no user":    {signInitData(testBotToken, map[string]string{"auth_date": strconv.FormatInt(now.Unix(), 10)}), ErrMalformed},
		"signed, user id zero":   {signInitData(testBotToken, map[string]string{"auth_date": strconv.FormatInt(now.Unix(), 10), "user": `{"id":0}`}), ErrMalformed},
		"signed, auth_date text": {signInitData(testBotToken, map[string]string{"auth_date": "yesterday", "user": `{"id":5}`}), ErrMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(tc.raw); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// The age of auth_date: at most an hour, and not from the future.
	for name, tc := range map[string]struct {
		at   time.Time
		want error
	}{
		"59 min 59 s old":      {now.Add(-MaxInitDataAge + time.Second), nil},
		"exactly an hour old":  {now.Add(-MaxInitDataAge), nil},
		"an hour and a second": {now.Add(-MaxInitDataAge - time.Second), ErrStale},
		"a day old":            {now.Add(-24 * time.Hour), ErrStale},
		"30 s ahead":           {now.Add(30 * time.Second), nil},
		"five minutes ahead":   {now.Add(5 * time.Minute), ErrStale},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(signInitData(testBotToken, miniAppFields(tc.at, 42)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMiniAppExchange(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	raw := signInitData(testBotToken, miniAppFields(h.clock.Now().Add(-5*time.Minute), testUserID))
	token := h.mini(b, raw, http.StatusOK)

	if len(token) != 43 {
		t.Errorf("token %q is %d characters", token, len(token))
	}
	if id, ok := b.signedIn(); !ok || id != testUserID {
		t.Fatalf("with the bearer token: %d, %v", id, ok)
	}
	if r := b.me(); r.code != http.StatusOK {
		t.Fatalf("/api/me with the bearer token: %d", r.code)
	}
	// core.touch got the person from the signed initData, with no chat.
	want := tg.User{ID: testUserID, FirstName: "Olesya", LastName: "K", Username: "olesya_u", LanguageCode: "uk"}
	if got := h.people.(*memPeople).users(); len(got) != 1 || got[0] != want {
		t.Fatalf("touched %+v", got)
	}
	// Only the hash is stored, as a Mini App token that lasts an hour.
	rows := h.sessions.(*memSessions).rows
	s, ok := rows[sha256.Sum256([]byte(token))]
	if len(rows) != 1 || !ok {
		t.Fatalf("rows = %+v", rows)
	}
	if s.UserID != testUserID || s.Kind != KindMiniApp || s.Expires.Sub(s.Created) != time.Hour {
		t.Errorf("row = %+v", s)
	}
	if strings.Contains(string(s.Hash[:]), token) {
		t.Error("the row holds the token")
	}

	// The same initData can be exchanged again within the hour; it is a
	// different token.
	b2 := h.browser()
	if token2 := h.mini(b2, raw, http.StatusOK); token2 == token {
		t.Error("two exchanges gave the same token")
	}
}

func TestMiniAppTokenExpires(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	h.mini(b, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusOK)
	h.clock.Advance(MiniAppTokenLife - time.Minute)
	if _, ok := b.signedIn(); !ok {
		t.Fatal("the token stopped working before its hour")
	}
	h.clock.Advance(2 * time.Minute)
	if _, ok := b.signedIn(); ok {
		t.Fatal("the token works after its hour")
	}
	if r := b.me(); r.code != http.StatusUnauthorized {
		t.Fatalf("/api/me with an expired token: %d", r.code)
	}
}

func TestMiniAppRefusals(t *testing.T) {
	now := time.Now()
	good := miniAppFields(now, testUserID)
	tampered := func() string {
		q, _ := url.ParseQuery(signInitData(testBotToken, good))
		q.Set("user", `{"id":1,"first_name":"Admin"}`)
		return q.Encode()
	}
	for name, tc := range map[string]struct {
		raw    string
		status int
		code   string
	}{
		"the user edited": {tampered(), 401, "init_data_invalid"},
		"another bot":     {signInitData("7000002:other", good), 401, "init_data_invalid"},
		"no hash":         {"auth_date=1&user=%7B%22id%22%3A1%7D", 401, "init_data_invalid"},
		"an hour old":     {signInitData(testBotToken, miniAppFields(now.Add(-61*time.Minute), testUserID)), 401, "init_data_expired"},
		"a day old":       {signInitData(testBotToken, miniAppFields(now.Add(-24*time.Hour), testUserID)), 401, "init_data_expired"},
		"from the future": {signInitData(testBotToken, miniAppFields(now.Add(10*time.Minute), testUserID)), 401, "init_data_expired"},
		"not initData":    {"hello", 401, "init_data_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			b := h.browser()
			body, _ := json.Marshal(map[string]string{"init_data": tc.raw})
			r := b.do(http.MethodPost, "/auth/miniapp", string(body))
			if r.code != tc.status || r.body != `{"error":"`+tc.code+"\"}\n" {
				t.Fatalf("%d %q, want %d %s", r.code, r.body, tc.status, tc.code)
			}
			if h.sessions.(*memSessions).count() != 0 || len(h.people.(*memPeople).users()) != 0 {
				t.Error("a refused initData left a session or touched core")
			}
			if _, ok := b.signedIn(); ok {
				t.Error("signed in")
			}
		})
	}

	h := newHarness(t)
	b := h.browser()
	big, _ := json.Marshal(map[string]string{"init_data": strings.Repeat("a", 20<<10)})
	for name, body := range map[string]string{
		"empty":               "",
		"not JSON":            "user=1",
		"no field":            `{}`,
		"empty initData":      `{"init_data":""}`,
		"initData not text":   `{"init_data":42}`,
		"over the body limit": string(big),
	} {
		r := b.do(http.MethodPost, "/auth/miniapp", body)
		if r.code != http.StatusBadRequest || r.body != "{\"error\":\"bad_request\"}\n" {
			t.Errorf("%s: %d %q", name, r.code, r.body)
		}
	}
	if r := b.get("/auth/miniapp"); r.code != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/miniapp: %d", r.code)
	}
}

func TestMiniAppCoreFailureMakesNoToken(t *testing.T) {
	h := newHarness(t, withPeople(&memPeople{err: errors.New("core is down")}))
	b := h.browser()
	h.mini(b, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusServiceUnavailable)
	if h.sessions.(*memSessions).count() != 0 {
		t.Error("a session without a person in core")
	}
}

// A token and a cookie are not interchangeable.
func TestTokenAndCookieAreDifferentCredentials(t *testing.T) {
	h := newHarness(t)
	site := h.browser()
	site.signIn("")
	cookie := site.cookies[SessionCookie]

	app := h.browser()
	token := h.mini(app, signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID)), http.StatusOK)
	// Both are known to the cache by now; it has to keep them apart too.
	site.signedIn()
	app.signedIn()

	// The cookie's value presented as a bearer token.
	asBearer := h.browser()
	asBearer.bearer = cookie
	if _, ok := asBearer.signedIn(); ok {
		t.Error("a site session works as a bearer token")
	}
	// The bearer token's value presented as the cookie.
	asCookie := h.browser()
	asCookie.cookies[SessionCookie] = token
	if _, ok := asCookie.signedIn(); ok {
		t.Error("a Mini App token works as the session cookie")
	}
}

func TestCrossSitePostsAreRefused(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.signIn("")
	raw := signInitData(testBotToken, miniAppFields(h.clock.Now(), testUserID))
	body, _ := json.Marshal(map[string]string{"init_data": raw})
	for _, path := range []string{"/auth/logout", "/auth/logout/all", "/auth/miniapp"} {
		r := b.do(http.MethodPost, path, string(body), "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example")
		if r.code != http.StatusForbidden {
			t.Errorf("cross-site POST %s: %d", path, r.code)
		}
	}
	if _, ok := b.signedIn(); !ok {
		t.Error("a cross-site POST ended the session")
	}
}

// The Mini App's sign-in keeps the name from initData and the photo, when
// the person's privacy settings let Telegram send it.
func TestMiniAppKeepsTheNameAndPhoto(t *testing.T) {
	h := newHarness(t)
	fields := miniAppFields(h.clock.Now(), testUserID)
	fields["user"] = `{"id":` + strconv.FormatInt(testUserID, 10) + `,"first_name":"Olesya","last_name":"K","username":"olesya_u","photo_url":"https://t.me/i/userpic/320/olesya.svg"}`
	h.mini(h.browser(), signInitData(testBotToken, fields), http.StatusOK)
	if got := h.people.(*memPeople).card(testUserID); got != [2]string{"Olesya K", "https://t.me/i/userpic/320/olesya.svg"} {
		t.Fatalf("card = %q", got)
	}
}
