// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// appSite is a Server with only the generated app on it.
func appSite(t *testing.T) (*site, *App) {
	t.Helper()
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{R2Endpoint: testR2, CSPExtra: app.CSP()})
	if err != nil {
		t.Fatal(err)
	}
	app.Mount(srv)
	return &site{Server: srv}, app
}

func TestAppPage(t *testing.T) {
	s, app := appSite(t)
	rr := s.get(t, "/")
	body := rr.Body.String()
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status %d, type %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	csp := rr.Header().Get("Content-Security-Policy")
	// The inline skeleton and boot script run because their hashes are in
	// the policy, and only those: no 'unsafe-inline', no nonce.
	for dir, hashes := range app.CSP() {
		if len(hashes) != 1 {
			t.Errorf("%s has %d inline blocks", dir, len(hashes))
		}
		for _, h := range hashes {
			if !strings.Contains(csp, dir+" 'self'") || !strings.Contains(csp, h) {
				t.Errorf("csp %q lacks %s %s", csp, dir, h)
			}
		}
	}
	if strings.Contains(csp, "unsafe") || strings.Contains(csp, "nonce") {
		t.Errorf("csp %q", csp)
	}
	// Nothing from outside but Telegram's script, which only the Mini App loads.
	for _, m := range regexp.MustCompile(`https?://[^"' )]+`).FindAllString(body, -1) {
		if m != "https://telegram.org/js/telegram-web-app.js" {
			t.Errorf("the page names %s", m)
		}
	}
	etag := rr.Header().Get("ETag")
	if etag == "" || rr.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("etag %q, cache %q", etag, rr.Header().Get("Cache-Control"))
	}
	rr = s.do(t, req{target: "/", header: map[string]string{"If-None-Match": etag}})
	if rr.Code != http.StatusNotModified || rr.Body.Len() != 0 {
		t.Errorf("revalidation: status %d, %d bytes", rr.Code, rr.Body.Len())
	}
	rr = s.do(t, req{method: http.MethodHead, target: "/"})
	if rr.Code != 200 || rr.Body.Len() != 0 || rr.Header().Get("Content-Security-Policy") != csp {
		t.Errorf("HEAD: status %d, headers %v", rr.Code, rr.Header())
	}
}

// The library and a run of it are the same page; an address that is not a
// job's number is not.
func TestAppViews(t *testing.T) {
	s, _ := appSite(t)
	page := s.get(t, "/").Body.String()
	for _, p := range []string{"/library", "/r/1", "/r/42"} {
		if rr := s.get(t, p); rr.Code != 200 || rr.Body.String() != page {
			t.Errorf("%s: status %d", p, rr.Code)
		}
	}
	for _, p := range []string{"/r/", "/r/abc", "/r/0", "/r/01", "/r/-1", "/r/1x", "/r/1234567890123456789", "/library/x"} {
		if rr := s.get(t, p); rr.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rr.Code)
		}
	}
}

func TestAppAssets(t *testing.T) {
	s, app := appSite(t)
	page := s.get(t, "/").Body.String()
	refs := regexp.MustCompile(`/assets/[^"']+`).FindAllString(page, -1)
	if len(refs) < 2 {
		t.Fatalf("the page names %v", refs)
	}
	types := map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png"}
	for _, ref := range refs {
		rr := s.get(t, ref)
		ext := ref[strings.LastIndex(ref, "."):]
		if rr.Code != 200 || rr.Header().Get("Cache-Control") != cacheImmutable || rr.Header().Get("Content-Type") != types[ext] {
			t.Errorf("%s: status %d, cache %q, type %q", ref, rr.Code, rr.Header().Get("Cache-Control"), rr.Header().Get("Content-Type"))
		}
	}
	// Every file of dist is named by its content, so all of them are cached for a year.
	for name, a := range app.assets {
		if !hashedName.MatchString(name) {
			t.Errorf("%s has no hash in its name", name)
		}
		rr := s.get(t, "/assets/"+name)
		if rr.Code != 200 || rr.Header().Get("Content-Type") != a.ctype || rr.Header().Get("Cache-Control") != cacheImmutable {
			t.Errorf("%s: status %d, type %q, cache %q", name, rr.Code, rr.Header().Get("Content-Type"), rr.Header().Get("Cache-Control"))
		}
	}
	for _, p := range []string{"/assets/", "/assets/missing.3f9a1c2b4d5e.js", "/assets/app.js", "/assets/../index.html"} {
		if rr := s.get(t, p); rr.Code == 200 {
			t.Errorf("%s: status %d", p, rr.Code)
		}
	}
	// Every language has its lines.
	n := 0
	for name := range app.assets {
		if strings.HasPrefix(name, "texts-") {
			n++
		}
	}
	if n != 16 {
		t.Errorf("%d languages of texts", n)
	}
}
