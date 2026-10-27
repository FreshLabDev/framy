// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"path"
	"regexp"
	"strings"
)

// Cloudflare caches by URL and by file extension, never by cookie, so the
// service says what may be cached and does not leave it to the handler.
//
//   - /api, /auth, /oauth, /events and /hooks answer no-store. /oauth is the
//     OAuth callbacks, which carry a code in the query. A handler's
//     `no-transform` stays; `public` goes.
//   - /assets/ holds files with a content hash in the name (app.3f9a1c2b.js)
//     or in a ?v= parameter. Those are `public, max-age=31536000, immutable`
//     on a success; a file without a hash is `no-cache`, and an error is
//     `no-store`, so a 404 is not kept for a year.
//   - Anywhere else a handler's own Cache-Control stands, except that
//     `public` is rewritten to `private, no-store` unless the route was
//     registered with PublicCache.
//   - Where a handler says nothing, HTML gets `no-cache` (the browser keeps
//     it and asks again; the ETag makes that cheap) and anything else
//     `no-store`, so a response is never cached by default.
const (
	cacheNoStore   = "no-store"
	cacheNoCache   = "no-cache"
	cacheImmutable = "public, max-age=31536000, immutable"
	cachePrivate   = "private, no-store"
)

// noStorePaths are the trees whose responses are never stored.
var noStorePaths = []string{"/api", "/auth", "/oauth", "/events", "/hooks"}

// hashedName matches a name with a run of hex digits before its extension:
// app.3f9a1c2b.js, sofia-sans-a1b2c3d4e5f6.woff2, 3f9a1c2b.css.
var hashedName = regexp.MustCompile(`(^|[.-])[0-9a-f]{8,64}(\.[A-Za-z0-9]+)+$`)

// hashedVersion matches the ?v= parameter that stands in for a hash.
var hashedVersion = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// cachePolicy sets the Cache-Control of a response about to be sent.
func cachePolicy(r *http.Request, status int, h http.Header, public bool) {
	p := path.Clean("/" + r.URL.Path)
	cc := strings.Join(h.Values("Cache-Control"), ", ")
	switch {
	case underAny(p, noStorePaths):
		switch {
		case hasDirective(cc, "public"):
			h.Set("Cache-Control", cacheNoStore)
		case cc == "":
			h.Set("Cache-Control", cacheNoStore)
		case !hasDirective(cc, "no-store"):
			// A stream's `no-cache, no-transform` keeps its no-transform.
			h.Set("Cache-Control", cc+", "+cacheNoStore)
		}
	case underPath(p, "/assets"):
		switch {
		case r.Method != http.MethodGet && r.Method != http.MethodHead,
			status != http.StatusOK && status != http.StatusPartialContent && status != http.StatusNotModified:
			h.Set("Cache-Control", cacheNoStore)
		case hashedName.MatchString(path.Base(p)) || hashedVersion.MatchString(r.URL.Query().Get("v")):
			h.Set("Cache-Control", cacheImmutable)
		default:
			h.Set("Cache-Control", cacheNoCache)
		}
	case cc == "":
		if isDocument(h.Get("Content-Type")) {
			h.Set("Cache-Control", cacheNoCache)
		} else {
			h.Set("Cache-Control", cacheNoStore)
		}
	case hasDirective(cc, "public") && !public:
		h.Set("Cache-Control", cachePrivate)
	}
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if underPath(p, d) {
			return true
		}
	}
	return false
}

// hasDirective reports whether a Cache-Control value has the directive,
// ignoring case and any =value.
func hasDirective(cc, name string) bool {
	for _, part := range strings.Split(cc, ",") {
		d, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}
