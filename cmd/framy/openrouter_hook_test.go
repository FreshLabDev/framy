// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/config"
	"github.com/FreshLabDev/framy/internal/web"
)

var hooksOn = config.Hooks{
	PublicURL:        "https://framy.example",
	PathSecret:       strings.Repeat("p4th-secret-", 4),
	OpenRouterSecret: "whsec-test",
}

func TestCallbacksFollowTheConfig(t *testing.T) {
	if cb := openRouterCallbacks(config.Hooks{}); cb.Enabled() {
		t.Error("callbacks are on with nothing configured")
	}
	if cb := openRouterCallbacks(config.Hooks{PublicURL: "https://framy.example"}); cb.Enabled() {
		t.Error("callbacks are on with no path secret")
	}
	cb := openRouterCallbacks(hooksOn)
	if !cb.Enabled() || !strings.HasPrefix(cb.URL(7), "https://framy.example/hooks/or/7/") {
		t.Errorf("callbacks %+v, url %s", cb, cb.URL(7))
	}
}

func hookServer(t *testing.T, cfg config.Hooks) (*web.Server, *bytes.Buffer) {
	t.Helper()
	site, err := web.New(web.Config{})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	// The route needs the database only after the path is right; a request
	// that stops before that never reaches the nil pool and queue.
	mountOpenRouterHook(site, cfg, nil, nil, slog.New(slog.NewTextHandler(logs, nil)))
	return site, logs
}

func TestTheHookIsOnlyMountedWhenCallbacksAre(t *testing.T) {
	site, logs := hookServer(t, config.Hooks{})
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hooks/or/7/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d with callbacks off", rec.Code)
	}
	if !strings.Contains(logs.String(), "callbacks are off") {
		t.Errorf("nothing said that callbacks are off:\n%s", logs)
	}
}

func TestAWarningSaysWhenFreeRunsWebhooksCannotBeChecked(t *testing.T) {
	cfg := hooksOn
	cfg.OpenRouterSecret = ""
	_, logs := hookServer(t, cfg)
	if !strings.Contains(logs.String(), "OPENROUTER_WEBHOOK_SECRET") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("no warning:\n%s", logs)
	}
	if _, quiet := hookServer(t, hooksOn); strings.Contains(quiet.String(), "WARN") {
		t.Errorf("a warning with the secret set:\n%s", quiet)
	}
}

// The hook takes a POST from another site (OpenRouter's servers send no
// Origin a browser would), refuses a path that is not the job's, and reads at
// most 64 KiB.
func TestTheHookTakesACrossSitePostAndChecksThePath(t *testing.T) {
	site, _ := hookServer(t, hooksOn)
	mac := openRouterCallbacks(hooksOn).MAC(7)

	post := func(path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		site.ServeHTTP(rec, req)
		return rec
	}

	rec := post("/hooks/or/7/"+strings.Repeat("A", 43), []byte("{}"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a wrong mac: status %d: the cross-site check must not stand in front of the hook", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q", cc)
	}
	rec = post(fmt.Sprintf("/hooks/or/7/%s", mac), bytes.Repeat([]byte("x"), 64<<10+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body above 64 KiB: status %d", rec.Code)
	}

	// And a cross-site POST to anything else that is not a hook is refused.
	site.HandleFunc("POST /api/v1/other", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if rec := post("/api/v1/other", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a cross-site POST to another route: status %d", rec.Code)
	}

	get := httptest.NewRecorder()
	site.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/hooks/or/7/"+mac, nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d", get.Code)
	}
}
