// SPDX-License-Identifier: Apache-2.0

package web

import "net/http"

// guard is the ResponseWriter every handler gets. The headers that depend on
// the response (the CSP on HTML, the cache rules, the missing
// X-Frame-Options) are decided at the moment the status goes out, when the
// handler has already said what it is sending, so no handler has to remember
// them and none can leave them out.
type guard struct {
	http.ResponseWriter
	r     *http.Request
	srv   *Server
	wrote bool
	// public is set by a route registered with PublicCache.
	public bool
}

func (g *guard) WriteHeader(status int) {
	// Interim responses (103 Early Hints) come before the final one; the
	// headers are decided once, for that.
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		g.ResponseWriter.WriteHeader(status)
		return
	}
	if !g.wrote {
		g.wrote = true
		g.finalize(status)
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *guard) Write(b []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	return g.ResponseWriter.Write(b)
}

// Flush sends the headers if nothing has, through the guard, so a stream that
// flushes before its first write still gets them. ResponseController asks for
// FlushError first.
func (g *guard) Flush() { _ = g.FlushError() }

func (g *guard) FlushError() error {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection for deadlines.
func (g *guard) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *guard) finalize(status int) {
	h := g.Header()
	// A frame-ancestors policy and X-Frame-Options together would refuse the
	// Telegram Web iframe, which is the Mini App.
	h.Del("X-Frame-Options")
	if isDocument(h.Get("Content-Type")) {
		h.Set("Content-Security-Policy", g.srv.csp)
	}
	cachePolicy(g.r, status, h, g.public)
}
