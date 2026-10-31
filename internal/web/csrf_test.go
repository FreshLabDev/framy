// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/metrics"
)

func TestCrossSitePostIsRefusedAndSameOriginIsNot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		target string
		header map[string]string
		want   int
	}{
		// The browser says it is cross-site.
		{"cross-site POST", "POST", "/api/ping", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"same-site POST from a sibling host", "POST", "/api/ping", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"cross-site PUT", "PUT", "/api/ping", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site DELETE", "DELETE", "/api/ping", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site PATCH", "PATCH", "/api/ping", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site POST to a page", "POST", "/page", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site POST to sign-in", "POST", "/auth/login", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site POST to the OAuth callback", "POST", "/oauth/openrouter/callback", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		// The same-origin ones: the Mini App's own fetches, the site's own forms.
		{"same-origin POST", "POST", "/api/ping", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://framy.test"}, 200},
		{"user-initiated POST", "POST", "/api/ping", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		// An older browser has no Sec-Fetch-Site; the Origin is compared with the Host.
		{"old browser, same origin", "POST", hostURL("/api/ping"), map[string]string{"Origin": "https://framy.test"}, 200},
		{"old browser, other origin", "POST", hostURL("/api/ping"), map[string]string{"Origin": "https://evil.example"}, 403},
		{"old browser, null origin", "POST", hostURL("/api/ping"), map[string]string{"Origin": "null"}, 403},
		// No browser headers at all: curl, a bot, another server.
		{"no browser headers", "POST", "/api/ping", nil, 200},
		// Safe methods are never refused, which is why nothing may change state on them.
		{"cross-site GET", "GET", "/api/ping", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"cross-site HEAD", "HEAD", "/api/ping", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"cross-site GET of a page", "GET", "/page", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, Config{})
			rr := s.do(t, req{method: tc.method, target: tc.target, header: tc.header})
			if rr.Code != tc.want {
				t.Fatalf("%s %s: status %d, want %d (%s)", tc.method, tc.target, rr.Code, tc.want, strings.TrimSpace(rr.Body.String()))
			}
			if tc.want == 403 && s.reached.Load() != 0 {
				t.Error("a refused request still reached its handler")
			}
		})
	}
}

func TestHooksTakeCrossSitePostsAndNothingElseDoes(t *testing.T) {
	cross := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://openrouter.ai"}
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/hooks/or/job1", 200},
		// The look-alikes of /hooks/ are not /hooks/.
		{"/hooks", 403},
		{"/hooksx/or/job1", 403},
		{"/Hooks/or/job1", 403},
		{"//hooks/or/job1", 403},
		// A path that climbs out of /hooks/ is not exempt: ServeMux would
		// redirect it, and the bypass applies only to a direct match.
		{"/hooks/../api/jobs", 403},
		{"/api/hooks/or/job1", 403},
		// An escaped ".." is a name, not a step up, to the bypass and to the
		// router alike: it stays inside /hooks/ and finds no handler there.
		{"/hooks/%2e%2e/api/jobs", 404},
	} {
		s := newSite(t, Config{})
		rr := s.do(t, req{method: http.MethodPost, target: tc.target, header: cross})
		if rr.Code != tc.want {
			t.Errorf("POST %s: status %d, want %d", tc.target, rr.Code, tc.want)
		}
		if tc.want != 200 && s.reached.Load() != 0 {
			t.Errorf("POST %s reached a handler", tc.target)
		}
	}
}

func TestCSRFRefusalsAreCounted(t *testing.T) {
	s := newSite(t, Config{})
	before := metricLine(t, "framy_http_cross_origin_refused_total")
	s.do(t, req{method: http.MethodPost, target: "/api/ping", header: map[string]string{"Sec-Fetch-Site": "cross-site"}})
	if after := metricLine(t, "framy_http_cross_origin_refused_total"); after != before+1 {
		t.Fatalf("counter %d -> %d", before, after)
	}
}

// metricLine reads one unlabeled counter from the registry's text output.
func metricLine(t *testing.T, name string) int {
	t.Helper()
	var sb strings.Builder
	metrics.Write(&sb)
	return metricValue(t, sb.String(), name)
}
