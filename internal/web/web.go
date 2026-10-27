// SPDX-License-Identifier: Apache-2.0

// Package web owns framy's public HTTP server: the router, the checks every
// request passes before a handler sees it, and the server's settings. The
// site, the Mini App, the API, the event streams, the OAuth callbacks and the
// providers' webhooks all plug into one Server, so none of them has to
// remember the security headers, the CSRF check, the cache rules or the rate
// limits. /metrics stays off this server, on its own listener.
//
// Every request goes through, in this order:
//
//  1. the client address: the TCP peer, or CF-Connecting-IP when the peer is
//     a trusted proxy (clientip.go);
//  2. the headers every response carries, and the guard that adds the CSP to
//     HTML, the cache rules to every response and removes X-Frame-Options
//     (headers.go, cache.go, guard.go);
//  3. the per-address limit of the default class (ratelimit.go);
//  4. http.CrossOriginProtection, with /hooks/ left out;
//  5. the mux, and the route's own wrapper: its stricter rate limit, its body
//     cap and its deadlines.
//
// Adding a route is a Handle call before the server starts:
//
//	site.Handle("GET /{$}", page)                          // an HTML page: Content-Type text/html is enough
//	site.Handle("POST /api/jobs", create, web.Limit(web.ClassJob))
//	site.Handle("POST /api/uploads", up, web.Limit(web.ClassUpload),
//		web.Body(100<<20), web.Deadlines(5*time.Minute, 5*time.Minute))
//	site.Handle("GET /api/jobs/{id}/events", sse, web.Stream()) // no deadlines: see Stream
//	site.Handle("POST /hooks/or/{job}/{mac}", hook)        // cross-site POST allowed, rate class hook
//	site.Handle("GET /assets/", assets)                    // files named app.<hash>.js, or ?v=<hash>
//
// A route under /api, /auth, /oauth, /events or /hooks answers `no-store`
// whatever its handler says. A route under /auth, /oauth, /hooks or
// /healthz takes its rate class from the path; anything else is in the
// default class unless Limit says otherwise.
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// Server settings. A route that needs longer asks for it with Deadlines,
// Stream or Body; the numbers here are for an ordinary API call.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	// idleTimeout is longer than cloudflared's keep-alive to the origin (90 s
	// by default): a connection the origin closed first would fail the next
	// request that cloudflared sends down it.
	idleTimeout = 120 * time.Second
	// maxHeaderBytes sits under Cloudflare's own limit on request headers.
	maxHeaderBytes = 32 << 10
	// defaultMaxBody is what a route may read unless it asks for more. The
	// API takes small JSON; uploads raise it with Body.
	defaultMaxBody = 1 << 20
)

// Config is what the Server needs from the rest of the service.
type Config struct {
	// Log receives the few things worth a line: a misconfigured proxy list
	// and the HTTP server's own errors. Nil discards them.
	Log *slog.Logger
	// R2Endpoint is the bucket's S3 endpoint (config.R2.Endpoint). Its origin
	// joins img-src, media-src and connect-src, since presigned files and
	// uploads go straight to it. Empty adds nothing.
	R2Endpoint string
	// CSPExtra adds sources to the policy, keyed by directive. The frontend
	// uses it for the hashes of its inline scripts and styles (InlineHash)
	// and for hosts such as the avatars' CDN. See buildCSP for the
	// directives it accepts.
	CSPExtra map[string][]string
	// TrustedProxies are the addresses cloudflared connects from. Only a
	// request whose TCP peer is in this list is believed when it says who the
	// visitor is (CF-Connecting-IP). Empty trusts nobody.
	TrustedProxies []netip.Prefix
	// Person names the signed-in person behind a request, or "" when there is
	// none. It is asked only on routes whose class has a per-person limit,
	// and has to be cheap: a map lookup, not a query per request. Nil keeps
	// every limit per address.
	Person func(*http.Request) string
	// Limits override DefaultLimits class by class.
	Limits Limits
	// Now is the limiter's clock, for tests.
	Now func() time.Time
}

// Server is the public listener's handler.
type Server struct {
	log     *slog.Logger
	mux     *http.ServeMux
	chain   http.Handler
	csp     string
	proxies []netip.Prefix
	person  func(*http.Request) string
	limits  map[Class]*classLimiter
	now     func() time.Time

	proxyWarned sync.Once
}

// csrfBypass lists the paths that accept cross-site POSTs: the providers'
// webhooks, which carry no browser's Origin and are checked by a signature in
// the path. Everything else a browser can post to is refused when it comes
// from another site.
var csrfBypass = []string{"/hooks/"}

// New builds a Server with no routes.
func New(cfg Config) (*Server, error) {
	csp, err := buildCSP(cfg.R2Endpoint, cfg.CSPExtra)
	if err != nil {
		return nil, err
	}
	limits, err := newLimiters(cfg.Limits)
	if err != nil {
		return nil, err
	}
	s := &Server{
		log:     cfg.Log,
		mux:     http.NewServeMux(),
		csp:     csp,
		proxies: cfg.TrustedProxies,
		person:  cfg.Person,
		limits:  limits,
		now:     cfg.Now,
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	cop := http.NewCrossOriginProtection()
	for _, p := range csrfBypass {
		cop.AddInsecureBypassPattern(p)
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.HTTPCrossOriginRefused.Inc()
		s.log.Debug("cross-origin request refused", "method", r.Method, "path", r.URL.Path)
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
	}))
	s.chain = cop.Handler(s.mux)
	return s, nil
}

// HTTPServer returns the *http.Server for addr, with the settings above.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
}

// Route options. They are read once, at Handle.
type routeOptions struct {
	class     Class
	maxBody   int64
	deadlines bool
	read      time.Duration
	write     time.Duration
	public    bool
}

// RouteOption changes how one route is served.
type RouteOption func(*routeOptions)

// Limit puts the route in a rate class, with that class's limits per address
// and per person on top of the default class every request is counted in.
func Limit(c Class) RouteOption { return func(o *routeOptions) { o.class = c } }

// Body raises the number of bytes the handler may read from the request body
// (1 MiB otherwise). Reading past it fails with *http.MaxBytesError.
func Body(max int64) RouteOption { return func(o *routeOptions) { o.maxBody = max } }

// Deadlines replaces the server's read and write deadlines on this route,
// counted from the moment the handler starts. Zero removes a deadline. An
// upload that takes minutes needs both; the response is written after the
// body is read, so write has to cover the wait.
func Deadlines(read, write time.Duration) RouteOption {
	return func(o *routeOptions) { o.deadlines, o.read, o.write = true, read, write }
}

// Stream is for server-sent events: it removes the write deadline, since a
// stream outlives any WriteTimeout (and the read deadline with it, which
// matters only when the request has a body). The handler keeps a dead or
// stalled client from holding it by setting a write deadline before each
// write:
//
//	rc := http.NewResponseController(w)
//	rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
//	fmt.Fprintf(w, ": ping\n\n")
//	rc.Flush()
//
// Send Cache-Control: no-store, no-transform; the cache rules keep the
// directives of a value that has no-store.
func Stream() RouteOption { return Deadlines(0, 0) }

// PublicCache lets the route's handler say `public`. Without it a response
// that says `public` is rewritten to `private, no-store`: Cloudflare caches
// by URL and never looks at a cookie, so `public` on anything a person owns
// would hand it to whoever asks for the same URL. Only routes whose bytes are
// the same for everyone, such as a public result image behind an unguessable
// token, may use it.
func PublicCache() RouteOption { return func(o *routeOptions) { o.public = true } }

// Handle registers h for pattern, which is a net/http.ServeMux pattern. It
// panics like ServeMux.Handle does on a conflict, and on a rate class that
// does not exist.
func (s *Server) Handle(pattern string, h http.Handler, opts ...RouteOption) {
	var o routeOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.class == "" {
		o.class = classOfPath(patternPath(pattern))
	}
	if s.limits[o.class] == nil {
		panic(fmt.Sprintf("web: %q is in an unknown rate class %q", pattern, o.class))
	}
	s.mux.Handle(pattern, s.route(h, o))
}

// HandleFunc is Handle for a function.
func (s *Server) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request), opts ...RouteOption) {
	s.Handle(pattern, http.HandlerFunc(h), opts...)
}

// route wraps a handler with what the route asked for.
func (s *Server) route(h http.Handler, o routeOptions) http.Handler {
	maxBody := o.maxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.class != ClassDefault && s.limited(w, r, o.class) {
			return
		}
		if g, ok := w.(*guard); ok && o.public {
			g.public = true
		}
		if o.deadlines {
			// The recorders of unit tests cannot set a deadline; a real
			// connection can, through the guard's Unwrap.
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(deadline(o.read))
			_ = rc.SetWriteDeadline(deadline(o.write))
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		h.ServeHTTP(w, r)
	})
}

func deadline(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d)
}

// ServeHTTP is the whole chain.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := s.client(r)
	r = r.WithContext(context.WithValue(r.Context(), clientKey{}, c))
	g := &guard{ResponseWriter: w, r: r, srv: s}
	setStaticHeaders(g.Header())
	if s.limited(g, r, ClassDefault) {
		return
	}
	s.chain.ServeHTTP(g, r)
}

// patternPath is the path part of a ServeMux pattern: "POST /api/jobs" and
// "example.com/api/" both give the path.
func patternPath(pattern string) string {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = strings.TrimLeft(pattern[i+1:], " \t")
	}
	if i := strings.IndexByte(pattern, '/'); i > 0 {
		pattern = pattern[i:]
	}
	return pattern
}

// underPath reports whether p is dir or inside it.
func underPath(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}
