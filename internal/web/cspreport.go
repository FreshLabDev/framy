// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// cspReportPath takes the browsers' reports of what the policy refused on a
// page: a script Cloudflare inserted, an extension's style, a source the
// policy lacks. The policy names it twice (buildCSP): report-uri, which every
// browser knows, and report-to with the Reporting-Endpoints header, which
// Chromium prefers.
const cspReportPath = "/csp-report"

// cspReportMax is the most of a report framy reads: one violation is a few
// hundred bytes, a batch of the Reporting API a few of them.
const cspReportMax = 16 << 10

// cspKnown are the directives a report is counted under; any other name is
// "other", so a forged report cannot grow the label set.
var cspKnown = []string{
	"default-src", "script-src", "script-src-elem", "script-src-attr", "style-src", "style-src-elem", "style-src-attr",
	"img-src", "media-src", "connect-src", "font-src", "frame-src", "child-src", "worker-src", "manifest-src",
	"object-src", "base-uri", "form-action", "frame-ancestors",
}

// cspReport counts a report by the directive it broke, and logs where the
// refused thing came from: its origin only, or the browser's word for it
// (inline, eval, data). The address of the page and the whole address of
// what was refused stay out, since either may carry a sign-in code or a
// token in its query.
func (s *Server) cspReport(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "report too large", http.StatusRequestEntityTooLarge)
		return
	}
	type one struct{ directive, blocked string }
	var got []one
	// report-uri: {"csp-report": {...}}, as application/csp-report.
	var old struct {
		Report *struct {
			Effective string `json:"effective-directive"`
			Violated  string `json:"violated-directive"`
			Blocked   string `json:"blocked-uri"`
		} `json:"csp-report"`
	}
	// report-to: a batch of reports, as application/reports+json.
	var batch []struct {
		Type string `json:"type"`
		Body struct {
			Effective string `json:"effectiveDirective"`
			Blocked   string `json:"blockedURL"`
		} `json:"body"`
	}
	switch {
	case json.Unmarshal(b, &old) == nil && old.Report != nil:
		d := old.Report.Effective
		if d == "" {
			d, _, _ = strings.Cut(old.Report.Violated, " ")
		}
		got = append(got, one{d, old.Report.Blocked})
	case json.Unmarshal(b, &batch) == nil:
		for _, x := range batch {
			if x.Type == "csp-violation" {
				got = append(got, one{x.Body.Effective, x.Body.Blocked})
			}
		}
	default:
		http.Error(w, "not a CSP report", http.StatusBadRequest)
		return
	}
	for _, x := range got {
		d := x.directive
		if !slices.Contains(cspKnown, d) {
			d = "other"
		}
		metrics.CSPViolations.Inc(d)
		s.log.Info("the page's policy refused something", "directive", d, "blocked", blockedOrigin(x.blocked))
	}
	w.WriteHeader(http.StatusNoContent)
}

// blockedOrigin is what a report says was refused, cut to what is safe to
// keep: the browser's keyword (inline, eval, wasm-eval, trusted-types-sink),
// the scheme of data: or blob: (which a browser may write bare), or the
// origin of an address.
func blockedOrigin(s string) string {
	switch s {
	case "inline", "eval", "wasm-eval", "trusted-types-policy", "trusted-types-sink", "data", "blob", "":
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" {
		return "unknown"
	}
	if u.Host == "" {
		return u.Scheme + ":"
	}
	return u.Scheme + "://" + u.Host
}
