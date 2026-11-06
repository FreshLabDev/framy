// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/metrics"
)

func cspCount(t *testing.T, directive string) int {
	t.Helper()
	var sb strings.Builder
	metrics.Write(&sb)
	return metricValue(t, sb.String(), `framy_csp_violations_total{directive="`+directive+`"}`)
}

// TestCSPReportsCount checks both forms a browser reports in: each is
// counted by its directive, a name framy does not use as other, and the log
// keeps where the refused thing came from but never a page's address or a
// query. A report from another site is taken: it only counts.
func TestCSPReportsCount(t *testing.T) {
	var logs bytes.Buffer
	s := newSite(t, Config{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	script, style, other := cspCount(t, "script-src-elem"), cspCount(t, "style-src"), cspCount(t, "other")
	for name, c := range map[string]struct {
		ctype, body string
		code        int
	}{
		"report-uri": {"application/csp-report",
			`{"csp-report":{"document-uri":"https://framy.example/auth/callback?code=SECRET","effective-directive":"script-src-elem","violated-directive":"script-src-elem","blocked-uri":"https://ajax.cloudflare.com/cdn-cgi/scripts/rocket-loader.min.js?token=SECRET"}}`, 204},
		"report-uri, violated only": {"application/csp-report", `{"csp-report":{"violated-directive":"style-src 'self'","blocked-uri":"inline"}}`, 204},
		"report-to": {"application/reports+json",
			`[{"type":"csp-violation","url":"https://framy.example/?code=SECRET","body":{"documentURL":"https://framy.example/?code=SECRET","effectiveDirective":"script-src-elem","blockedURL":"inline"}},{"type":"deprecation","body":{}},{"type":"csp-violation","body":{"effectiveDirective":"made-up-src","blockedURL":"data"}}]`, 204},
		"junk": {"application/csp-report", `not json`, 400},
	} {
		rr := s.do(t, req{method: http.MethodPost, target: cspReportPath, body: c.body,
			header: map[string]string{"Content-Type": c.ctype, "Sec-Fetch-Site": "cross-site", "Origin": "null"}})
		if rr.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rr.Code, c.code)
		}
	}
	if d := cspCount(t, "script-src-elem") - script; d != 2 {
		t.Errorf("script-src-elem counted %d, want 2", d)
	}
	if d := cspCount(t, "style-src") - style; d != 1 {
		t.Errorf("style-src counted %d, want 1", d)
	}
	if d := cspCount(t, "other") - other; d != 1 {
		t.Errorf("other counted %d, want 1", d)
	}
	l := logs.String()
	if strings.Contains(l, "SECRET") || strings.Contains(l, "callback") || !strings.Contains(l, "blocked=https://ajax.cloudflare.com\n") || !strings.Contains(l, "blocked=inline") || !strings.Contains(l, "blocked=data") {
		t.Errorf("the log: %s", l)
	}
	// A report past the size of a few is refused unread.
	if rr := s.do(t, req{method: http.MethodPost, target: cspReportPath, body: `[` + strings.Repeat(`{"type":"x"},`, 2000) + `{}]`}); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a huge report: %d", rr.Code)
	}
}

// TestPagesNameTheReportEndpoint checks a page says where its policy
// reports, and that the API and the files are for framy's pages alone.
func TestPagesNameTheReportEndpoint(t *testing.T) {
	s := newSite(t, Config{})
	page := s.get(t, "/")
	if got := page.Header().Get("Reporting-Endpoints"); got != `csp="/csp-report"` {
		t.Errorf("Reporting-Endpoints = %q", got)
	}
	if got := page.Header().Get("Cross-Origin-Resource-Policy"); got != "" {
		t.Errorf("a page carries CORP %q", got)
	}
	if got := s.get(t, "/api/ping").Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("an API answer: CORP %q", got)
	}
	if got := s.get(t, "/api/ping").Header().Get("Reporting-Endpoints"); got != "" {
		t.Errorf("an API answer names a report endpoint: %q", got)
	}
}
