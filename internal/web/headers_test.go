// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"strings"
	"testing"
)

// The policy, written out. A change to it has to be a change to this test.
const wantCSP = "default-src 'none'; " +
	"script-src 'self' https://telegram.org/js/telegram-web-app.js; " +
	"style-src 'self'; " +
	"img-src 'self' data: blob: https://t.me https://*.telesco.pe https://abc123.r2.cloudflarestorage.com; " +
	"media-src 'self' blob: https://abc123.r2.cloudflarestorage.com; " +
	"connect-src 'self' https://abc123.r2.cloudflarestorage.com; " +
	"font-src 'self'; " +
	"base-uri 'none'; object-src 'none'; form-action 'self'; " +
	"frame-ancestors https://web.telegram.org https://webk.telegram.org https://weba.telegram.org"

func TestCSPIsTheDesignsPolicy(t *testing.T) {
	got, err := buildCSP(testR2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantCSP {
		t.Fatalf("policy =\n%s\nwant\n%s", got, wantCSP)
	}
}

func TestCSPTakesTheR2OriginFromTheEndpoint(t *testing.T) {
	// A path or a bucket in the endpoint is not part of an origin.
	got, err := buildCSP("https://files.example.test:9000/framy", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"img-src", "media-src", "connect-src"} {
		if !strings.Contains(got, d+" ") || !strings.Contains(got, "https://files.example.test:9000") {
			t.Errorf("%s lacks the endpoint's origin: %s", d, got)
		}
	}
	if strings.Contains(got, "/framy") {
		t.Errorf("a path leaked into the policy: %s", got)
	}
	// Without an endpoint nothing is allowed that is not the site's own.
	got, err = buildCSP("", nil)
	if err != nil || strings.Contains(got, "r2.") || !strings.Contains(got, "connect-src 'self';") {
		t.Errorf("no endpoint: %q, %v", got, err)
	}
	if _, err := buildCSP("ftp://x.example", nil); err == nil {
		t.Error("a non-http endpoint was accepted")
	}
}

func TestCSPExtraSources(t *testing.T) {
	css := "body{margin:0}"
	got, err := buildCSP(testR2, map[string][]string{
		"style-src": {InlineHash(css)},
		"img-src":   {"https://cdn4.telesco.pe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "style-src 'self' 'sha256-") || !strings.Contains(got, "https://cdn4.telesco.pe;") {
		t.Errorf("extras are missing: %s", got)
	}
	// The hash is the one a browser computes: SHA-256 of the text, base64.
	if want := "'sha256-"; !strings.HasPrefix(InlineHash(css), want) || len(InlineHash(css)) != len(want)+44+1 {
		t.Errorf("InlineHash = %s", InlineHash(css))
	}
	for name, extra := range map[string]map[string][]string{
		"a directive that is not extendable": {"frame-ancestors": {"https://evil.example"}},
		"the default":                        {"default-src": {"*"}},
		"an unknown directive":               {"script-source": {"'self'"}},
		"a source that ends the directive":   {"img-src": {"https://a.example; script-src *"}},
		"a source with a space":              {"img-src": {"a b"}},
		"an empty source":                    {"img-src": {""}},
	} {
		if _, err := buildCSP(testR2, extra); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCSPFramesOnlyTelegramWeb(t *testing.T) {
	s := newSite(t, Config{})
	rr := s.get(t, "/")
	csp := rr.Header().Get("Content-Security-Policy")
	var fa string
	for _, d := range strings.Split(csp, "; ") {
		if strings.HasPrefix(d, "frame-ancestors ") {
			fa = strings.TrimPrefix(d, "frame-ancestors ")
		}
	}
	if fa != "https://web.telegram.org https://webk.telegram.org https://weba.telegram.org" {
		t.Fatalf("frame-ancestors = %q", fa)
	}
}

func TestCSPIsOnEveryHTMLResponseAndOnlyThose(t *testing.T) {
	s := newSite(t, Config{})
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"/", true},
		{"/page", true},
		{"/r/private", true},
		// No Content-Type: net/http will sniff HTML from the body.
		{"/typeless", true},
		{"/svg", true},
		{"/api/ping", false},
		{"/healthz", false},
		{"/assets/app.3f9a1c2b.js", false},
	} {
		got := s.get(t, tc.target).Header().Get("Content-Security-Policy")
		if (got != "") != tc.want {
			t.Errorf("%s: CSP = %q, want present=%v", tc.target, got, tc.want)
		}
		if tc.want && got != wantCSP {
			t.Errorf("%s: CSP = %q", tc.target, got)
		}
	}
}

func TestNoXFrameOptionsEver(t *testing.T) {
	s := newSite(t, Config{})
	// A handler that sets one anyway loses it: it would refuse the iframe of
	// Telegram Web that the CSP lets in.
	for _, target := range []string{"/", "/framed", "/page", "/api/ping", "/nope"} {
		if got := s.get(t, target).Header().Get("X-Frame-Options"); got != "" {
			t.Errorf("%s: X-Frame-Options = %q", target, got)
		}
	}
}

func TestStaticHeadersOnEveryResponse(t *testing.T) {
	s := newSite(t, Config{})
	tight := newSite(t, Config{Limits: Limits{ClassDefault: {IP: perSecond(1, 1)}}})
	tight.get(t, "/page") // spends the one token
	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Strict-Transport-Security":  "max-age=31536000",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for name, c := range map[string]struct {
		site *site
		req  req
		code int
	}{
		"a page":               {s, req{target: "/"}, 200},
		"an API answer":        {s, req{target: "/api/ping"}, 200},
		"a missing path":       {s, req{target: "/nope"}, 404},
		"a wrong method":       {s, req{method: http.MethodPost, target: "/"}, 405},
		"a refused cross-site": {s, req{method: http.MethodPost, target: "/api/ping", header: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403},
		"a rate-limited reply": {tight, req{target: "/page"}, 429},
	} {
		rr := c.site.do(t, c.req)
		if rr.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rr.Code, c.code)
		}
		for k, v := range want {
			if got := rr.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", name, k, got, v)
			}
		}
	}
}
