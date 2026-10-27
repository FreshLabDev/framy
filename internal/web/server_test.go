// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerSettings(t *testing.T) {
	s := newSite(t, Config{})
	hs := s.HTTPServer(":8080")
	if hs.Addr != ":8080" || hs.Handler != http.Handler(s.Server) {
		t.Fatalf("addr %q, handler %T", hs.Addr, hs.Handler)
	}
	if hs.ReadHeaderTimeout != 5*time.Second || hs.ReadTimeout != 10*time.Second || hs.WriteTimeout != 10*time.Second {
		t.Errorf("timeouts: header %v, read %v, write %v", hs.ReadHeaderTimeout, hs.ReadTimeout, hs.WriteTimeout)
	}
	// cloudflared keeps connections to the origin for 90 s by default; ours
	// must outlast that or it closes one cloudflared is about to reuse.
	if hs.IdleTimeout <= 90*time.Second {
		t.Errorf("IdleTimeout %v is not above cloudflared's keep-alive", hs.IdleTimeout)
	}
	if hs.MaxHeaderBytes <= 0 || hs.MaxHeaderBytes > 64<<10 {
		t.Errorf("MaxHeaderBytes = %d", hs.MaxHeaderBytes)
	}
	if hs.ErrorLog == nil {
		t.Error("the server's own errors bypass the structured log")
	}
}

func TestPlaceholderPage(t *testing.T) {
	s := newSite(t, Config{})
	rr := s.get(t, "/")
	body := rr.Body.String()
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status %d, type %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, "framy is coming") || !strings.Contains(body, `lang="en"`) {
		t.Errorf("body = %s", body)
	}
	// It has to work under the CSP, with nothing from outside.
	for _, bad := range []string{"<script", "<link", "<style", "http://", "https://", " style="} {
		if strings.Contains(body, bad) {
			t.Errorf("the placeholder contains %q", bad)
		}
	}
	etag := rr.Header().Get("ETag")
	if etag == "" || rr.Header().Get("Cache-Control") != "no-cache" || rr.Header().Get("Content-Security-Policy") != wantCSP {
		t.Errorf("etag %q, cache %q, csp %q", etag, rr.Header().Get("Cache-Control"), rr.Header().Get("Content-Security-Policy"))
	}
	// A revalidation costs a 304.
	rr = s.do(t, req{target: "/", header: map[string]string{"If-None-Match": etag}})
	if rr.Code != http.StatusNotModified || rr.Body.Len() != 0 {
		t.Errorf("revalidation: status %d, %d bytes", rr.Code, rr.Body.Len())
	}
	// HEAD, which is what `curl -I` sends, carries the same headers.
	rr = s.do(t, req{method: http.MethodHead, target: "/"})
	if rr.Code != 200 || rr.Body.Len() != 0 || rr.Header().Get("Content-Security-Policy") != wantCSP ||
		rr.Header().Get("Strict-Transport-Security") == "" {
		t.Errorf("HEAD: status %d, headers %v", rr.Code, rr.Header())
	}
	if rr := s.do(t, req{method: http.MethodPost, target: "/"}); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d", rr.Code)
	}
}

func TestHealthzKeepsItsBehaviour(t *testing.T) {
	srv, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	srv.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false}` + "\n"))
	}))
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	srv.ServeHTTP(rr, r)
	if rr.Code != 503 || rr.Body.String() != `{"ok":false}`+"\n" || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, body %q, type %q", rr.Code, rr.Body.String(), rr.Header().Get("Content-Type"))
	}
	// It is JSON, not a page: no CSP, and the answer is not cached.
	if rr.Header().Get("Content-Security-Policy") != "" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("csp %q, cache %q", rr.Header().Get("Content-Security-Policy"), rr.Header().Get("Cache-Control"))
	}
	// Docker's healthcheck comes from inside the container every 15 s and
	// must never be limited; a flood from one address is.
	for i := 0; i < 9; i++ {
		rr = httptest.NewRecorder()
		srv.ServeHTTP(rr, r)
	}
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, r)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("the 11th check in a burst: %d, want 429", rr.Code)
	}
}

func TestBodyCap(t *testing.T) {
	s := newSite(t, Config{})
	read := func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	}
	s.HandleFunc("POST /api/small", read)
	s.HandleFunc("POST /api/big", read, Body(8<<20))
	body := strings.Repeat("x", 2<<20)
	if rr := s.do(t, req{method: "POST", target: "/api/small", body: body}); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("2 MiB to an ordinary route: %d, want 413", rr.Code)
	}
	if rr := s.do(t, req{method: "POST", target: "/api/small", body: body[:1<<20]}); rr.Code != 200 {
		t.Errorf("1 MiB to an ordinary route: %d", rr.Code)
	}
	if rr := s.do(t, req{method: "POST", target: "/api/big", body: body}); rr.Code != 200 {
		t.Errorf("2 MiB to a route that asked for 8: %d", rr.Code)
	}
}

// startServer runs the Server on a real listener with short deadlines, since
// deadlines exist only on a real connection.
func startServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(s)
	ts.Config.ReadTimeout = 300 * time.Millisecond
	ts.Config.WriteTimeout = 300 * time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// ticker is a handler that sends six events 150 ms apart: 900 ms in all, three
// times the server's deadlines. With renew it sets a write deadline before
// each event, as a stream is meant to; without, it relies on the route.
func ticker(renew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store, no-transform")
		rc := http.NewResponseController(w)
		w.WriteHeader(http.StatusOK)
		_ = rc.Flush()
		for i := 0; i < 6; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
			if renew {
				_ = rc.SetWriteDeadline(time.Now().Add(time.Second))
			}
			if _, err := fmt.Fprintf(w, "id: %d\ndata: tick\n\n", i); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func countEvents(t *testing.T, url string) (events int, cacheControl string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data:") {
			events++
		}
	}
	return events, resp.Header.Get("Cache-Control")
}

func TestStreamRouteOutlivesTheServerDeadlines(t *testing.T) {
	s := newSite(t, Config{})
	s.HandleFunc("GET /api/jobs/{id}/events", ticker(false), Stream())
	// A handler can also keep its own deadline fresh, through the guard.
	s.HandleFunc("GET /api/renewed/events", ticker(true))
	// The control: the same handler as the first, on an ordinary route, is cut
	// off by the server's own deadline, so the test shows what Stream changes.
	s.HandleFunc("GET /api/cut/events", ticker(false))
	ts := startServer(t, s.Server)

	events, cc := countEvents(t, ts.URL+"/api/jobs/7/events")
	if events != 6 {
		t.Errorf("a Stream route delivered %d of 6 events past the server's deadlines", events)
	}
	if cc != "no-store, no-transform" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if events, _ := countEvents(t, ts.URL+"/api/renewed/events"); events != 6 {
		t.Errorf("a handler renewing its write deadline delivered %d of 6 events", events)
	}
	if events, _ := countEvents(t, ts.URL+"/api/cut/events"); events >= 6 {
		t.Errorf("an ordinary route delivered all %d events; the deadlines are not in force", events)
	}
}

func TestStreamHeadersAreSentWithoutWaitingForTheFirstEvent(t *testing.T) {
	// Cloudflare counts its 125 s from the request to the first byte, so the
	// headers must go at once. A Flush before any write is enough, and the
	// guard has to decide the headers on that path too.
	s := newSite(t, Config{})
	s.HandleFunc("GET /events/x/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Frame-Options", "DENY")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
		<-r.Context().Done()
	}, Stream())
	ts := startServer(t, s.Server)
	resp, err := http.Get(ts.URL + "/events/x/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("X-Frame-Options") != "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status %d, headers %v", resp.StatusCode, resp.Header)
	}
}

func TestDeadlinesLetASlowUploadFinish(t *testing.T) {
	s := newSite(t, Config{})
	count := func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusRequestTimeout)
			return
		}
		fmt.Fprintf(w, "%d", n)
	}
	s.HandleFunc("POST /api/slow", count)
	s.HandleFunc("POST /api/uploads/slow", count, Body(1<<20), Deadlines(3*time.Second, 3*time.Second))
	ts := startServer(t, s.Server)

	// Seven chunks, 100 ms apart: slower than the server's 300 ms.
	post := func(path string) (int, string) {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 7; i++ {
				time.Sleep(100 * time.Millisecond)
				if _, err := pw.Write([]byte("0123456789")); err != nil {
					return
				}
			}
			pw.Close()
		}()
		resp, err := http.Post(ts.URL+path, "application/octet-stream", pr)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := post("/api/uploads/slow"); code != 200 || body != "70" {
		t.Errorf("a route with Deadlines: status %d, body %q", code, body)
	}
	if code, body := post("/api/slow"); code == 200 {
		t.Errorf("an ordinary route took a body slower than its ReadTimeout: %q", body)
	}
}

func TestNoDeadlineOptionOnARecorderIsHarmless(t *testing.T) {
	// httptest's recorder cannot set a deadline; the route must still serve.
	s := newSite(t, Config{})
	s.HandleFunc("GET /api/rec/events", ticker(true), Stream())
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/rec/events", nil)
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	s.ServeHTTP(rr, r.WithContext(ctx))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "tick") {
		t.Fatalf("status %d, body %q", rr.Code, rr.Body.String())
	}
}
