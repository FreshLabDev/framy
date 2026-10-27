// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testR2 = "https://abc123.r2.cloudflarestorage.com"

// clock is a Config.Now that moves only when told to.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_800_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// site is a Server with the routes the tests use, each answering the way a
// real handler of its kind would, and a count of how many requests reached a
// handler at all.
type site struct {
	*Server
	reached atomic.Int64
}

func newSite(t *testing.T, cfg Config) *site {
	t.Helper()
	if cfg.R2Endpoint == "" {
		cfg.R2Endpoint = testR2
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{Server: srv}
	json := func(w http.ResponseWriter, _ *http.Request) {
		s.reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	html := func(cc string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			s.reached.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if cc != "" {
				w.Header().Set("Cache-Control", cc)
			}
			_, _ = w.Write([]byte("<!doctype html><title>t</title>"))
		}
	}
	srv.Handle("GET /{$}", Placeholder())
	srv.HandleFunc("/healthz", json)
	srv.HandleFunc("/api/ping", json)
	srv.HandleFunc("/api/jobs", json, Limit(ClassJob))
	srv.HandleFunc("/api/uploads", json, Limit(ClassUpload))
	srv.HandleFunc("/api/leaky", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = w.Write([]byte(`{}`))
	})
	srv.HandleFunc("/api/stream-ish", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		_, _ = w.Write([]byte(": ping\n\n"))
	})
	srv.HandleFunc("/auth/login", json)
	srv.HandleFunc("/auth/miniapp", json)
	srv.HandleFunc("/oauth/openrouter/callback", json)
	srv.HandleFunc("/events/x", json)
	srv.HandleFunc("/hooks/or/{job}", json)
	srv.HandleFunc("/page", html(""))
	srv.HandleFunc("/r/private", html("public, max-age=300"))
	srv.HandleFunc("/r/shared", html("public, max-age=60"), PublicCache())
	srv.HandleFunc("/typeless", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<p>hi</p>"))
	})
	srv.HandleFunc("/svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte("<svg/>"))
	})
	srv.HandleFunc("/framed", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<p>x</p>"))
	})
	srv.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		s.reached.Add(1)
		switch {
		case strings.Contains(r.URL.Path, "missing"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Content-Type", "text/javascript")
		case strings.HasSuffix(r.URL.Path, ".woff2"):
			w.Header().Set("Content-Type", "font/woff2")
		}
		if strings.Contains(r.URL.Path, "range") {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write([]byte("asset"))
	})
	return s
}

// req is one request to a Server, with the headers a test cares about.
type req struct {
	method, target string
	remote         string
	header         map[string]string
	body           string
}

func (s *site) do(t *testing.T, r req) *httptest.ResponseRecorder {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	hr := httptest.NewRequest(r.method, r.target, strings.NewReader(r.body))
	if r.remote == "" {
		r.remote = "203.0.113.7:40000"
	}
	hr.RemoteAddr = r.remote
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, hr)
	return rr
}

func (s *site) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return s.do(t, req{target: target})
}

// hostURL makes a request target on a host, for the checks that compare the
// Origin with the Host.
func hostURL(path string) string { return "http://framy.test" + path }
