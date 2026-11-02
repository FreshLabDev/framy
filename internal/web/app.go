// SPDX-License-Identifier: Apache-2.0

package web

//go:generate go run ./build

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// dist is what go generate makes from app/: the page and its assets, each
// asset named by its content.
//
//go:embed all:dist
var dist embed.FS

// App is framy's site and Mini App: the page at / and its files under
// /assets/. The page carries its skeleton style and boot script inline, so
// its first paint needs no other request; their hashes go into the CSP.
type App struct {
	page   []byte
	etag   string
	csp    map[string][]string
	assets map[string]asset
}

type asset struct {
	body  []byte
	ctype string
}

// assetTypes are the types of the files the generator writes. A file of
// another kind is a mistake in the generator and fails NewApp.
var assetTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".woff2": "font/woff2",
}

// inline finds the bodies of the page's inline <style> and <script> blocks;
// a script with a src is loaded, not inline.
var inline = regexp.MustCompile(`(?s)<(style|script)>(.*?)</(?:style|script)>`)

// NewApp reads the generated page and assets.
func NewApp() (*App, error) {
	page, err := dist.ReadFile("dist/index.html")
	if err != nil {
		return nil, errors.New("web: no dist/index.html; run go generate ./internal/web")
	}
	a := &App{page: page, csp: map[string][]string{}, assets: map[string]asset{}}
	for _, m := range inline.FindAllSubmatch(page, -1) {
		dir := "style-src"
		if string(m[1]) == "script" {
			dir = "script-src"
		}
		a.csp[dir] = append(a.csp[dir], InlineHash(string(m[2])))
	}
	if len(a.csp["style-src"]) == 0 || len(a.csp["script-src"]) == 0 {
		return nil, errors.New("web: dist/index.html has no inline skeleton or boot script")
	}
	sum := sha256.Sum256(page)
	a.etag = `"` + hex.EncodeToString(sum[:8]) + `"`

	err = fs.WalkDir(dist, "dist/assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ct := assetTypes[path.Ext(p)]
		if ct == "" {
			return fmt.Errorf("web: %s has no known type", p)
		}
		body, err := dist.ReadFile(p)
		if err != nil {
			return err
		}
		a.assets[path.Base(p)] = asset{body: body, ctype: ct}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Every asset the page names must be there.
	for _, ref := range regexp.MustCompile(`/assets/([^"']+)`).FindAllSubmatch(page, -1) {
		if _, ok := a.assets[string(ref[1])]; !ok {
			return nil, fmt.Errorf("web: the page names /assets/%s, which was not generated", ref[1])
		}
	}
	return a, nil
}

// CSP is the sources the page needs beyond the policy's own: the hashes of
// its inline blocks. Pass it as Config.CSPExtra.
func (a *App) CSP() map[string][]string {
	out := make(map[string][]string, len(a.csp))
	for k, v := range a.csp {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Mount registers the page and the assets on the server. The page answers
// at the addresses of its views too: the library, and one run of it at
// /r/<job id>, which the page opens for whoever owns that job.
func (a *App) Mount(s *Server) {
	s.Handle("GET /{$}", http.HandlerFunc(a.servePage))
	s.Handle("GET /library", http.HandlerFunc(a.servePage))
	s.Handle("GET /r/{id}", http.HandlerFunc(a.serveRun))
	s.Handle("GET /assets/", http.HandlerFunc(a.serveAsset))
}

// serveRun answers the page for /r/<id> when the id is a job's number, and a
// 404 for anything else.
func (a *App) serveRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 18 || strings.TrimLeft(id, "0123456789") != "" || id[0] == '0' {
		http.NotFound(w, r)
		return
	}
	a.servePage(w, r)
}

// servePage answers the page. The cache rules make it no-cache, so a browser
// asks again every time and gets a 304 while the page has not changed.
func (a *App) servePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", a.etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.page))
}

// serveAsset answers one file by its hashed name; anything else is a 404
// (no listing of /assets/).
func (a *App) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	f, ok := a.assets[name]
	if !ok || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.ctype)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
}
