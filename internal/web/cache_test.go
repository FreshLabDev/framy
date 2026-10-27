// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"testing"
)

func TestCacheControlByRoute(t *testing.T) {
	s := newSite(t, Config{})
	for _, tc := range []struct {
		name   string
		req    req
		status int
		want   string
	}{
		// no-store on the trees that hold anything private.
		{"api", req{target: "/api/ping"}, 200, "no-store"},
		{"auth", req{target: "/auth/login"}, 200, "no-store"},
		{"oauth callback", req{target: "/oauth/openrouter/callback"}, 200, "no-store"},
		{"events", req{target: "/events/x"}, 200, "no-store"},
		{"hooks", req{method: http.MethodPost, target: "/hooks/or/job1"}, 200, "no-store"},
		{"api error", req{target: "/api/nope"}, 404, "no-store"},
		{"api that tries to be public", req{target: "/api/leaky"}, 200, "no-store"},
		// A stream's own directives stay beside no-store.
		{"stream headers", req{target: "/api/stream-ish"}, 200, "no-cache, no-transform, no-store"},
		// The step up out of a no-store tree is still that tree.
		{"unclean path into api", req{target: "/assets/../api/ping"}, 307, "no-store"},

		// Hashed assets: a year, immutable.
		{"hashed name", req{target: "/assets/app.3f9a1c2b.js"}, 200, "public, max-age=31536000, immutable"},
		{"hashed font", req{target: "/assets/sofia-sans-a1b2c3d4e5f6.woff2"}, 200, "public, max-age=31536000, immutable"},
		{"name that is only a hash", req{target: "/assets/3f9a1c2b4d5e.css"}, 200, "public, max-age=31536000, immutable"},
		{"?v= hash", req{target: "/assets/app.js?v=3f9a1c2b"}, 200, "public, max-age=31536000, immutable"},
		{"hashed, ranged", req{target: "/assets/range.3f9a1c2b.js"}, 206, "public, max-age=31536000, immutable"},
		{"hashed, HEAD", req{method: http.MethodHead, target: "/assets/app.3f9a1c2b.js"}, 200, "public, max-age=31536000, immutable"},
		// ... but only a hashed file, and only a success.
		{"unhashed asset", req{target: "/assets/app.js"}, 200, "no-cache"},
		{"short hash", req{target: "/assets/app.3f9a.js"}, 200, "no-cache"},
		{"non-hex v", req{target: "/assets/app.js?v=latest"}, 200, "no-cache"},
		{"hashed but missing", req{target: "/assets/missing.3f9a1c2b.js"}, 404, "no-store"},
		{"hashed, wrong method", req{method: http.MethodPost, target: "/assets/app.3f9a1c2b.js", header: map[string]string{"Sec-Fetch-Site": "same-origin"}}, 200, "no-store"},

		// Pages revalidate; nothing is cached because nobody said.
		{"placeholder", req{target: "/"}, 200, "no-cache"},
		{"html page", req{target: "/page"}, 200, "no-cache"},
		{"health", req{target: "/healthz"}, 200, "no-store"},
		{"a missing path", req{target: "/nope"}, 404, "no-store"},
		// A person's result is never public, whatever the handler hoped.
		{"private result that says public", req{target: "/r/private"}, 200, "private, no-store"},
		// Only a route that opted in may say public.
		{"public result", req{target: "/r/shared"}, 200, "public, max-age=60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := s.do(t, tc.req)
			if rr.Code != tc.status {
				t.Fatalf("status %d, want %d", rr.Code, tc.status)
			}
			if got := rr.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNothingPrivateIsEverPublic(t *testing.T) {
	// Over every route of the test site: a response that says `public` is on
	// a route that opted in or in the hashed assets, and nowhere else.
	s := newSite(t, Config{})
	public := map[string]bool{"/r/shared": true, "/assets/app.3f9a1c2b.js": true}
	for _, target := range []string{"/", "/page", "/api/ping", "/api/leaky", "/auth/login", "/events/x", "/healthz",
		"/r/private", "/r/shared", "/typeless", "/svg", "/framed", "/nope", "/assets/app.js", "/assets/app.3f9a1c2b.js"} {
		cc := s.get(t, target).Header().Get("Cache-Control")
		if hasDirective(cc, "public") != public[target] {
			t.Errorf("%s: Cache-Control = %q", target, cc)
		}
	}
}

func TestHasDirective(t *testing.T) {
	for _, tc := range []struct {
		cc, name string
		want     bool
	}{
		{"public, max-age=60", "public", true},
		{"PUBLIC", "public", true},
		{"max-age=60, Public", "public", true},
		{"private", "public", false},
		{"s-maxage=60", "max-age", false},
		{"no-store, no-transform", "no-store", true},
		{"no-cache", "no-store", false},
		{"", "no-store", false},
	} {
		if got := hasDirective(tc.cc, tc.name); got != tc.want {
			t.Errorf("hasDirective(%q, %q) = %v", tc.cc, tc.name, got)
		}
	}
}
