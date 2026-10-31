// SPDX-License-Identifier: Apache-2.0

package web

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// telegramWeb are the origins that may frame the site: the three hosts of
// Telegram Web, where the Mini App opens in an iframe. Native clients use a
// webview, to which frame-ancestors does not apply. This is the only list
// of its kind; the design has no X-Frame-Options, which would refuse these
// same frames.
var telegramWeb = []string{"https://web.telegram.org", "https://webk.telegram.org", "https://weba.telegram.org"}

// telegramScript is the one script that does not come from the site itself.
const telegramScript = "https://telegram.org/js/telegram-web-app.js"

// telegramPhotos are where a person's photo comes from: the link Telegram
// gives at sign-in is on t.me and redirects to its CDN, and a CSP checks
// the host after a redirect too. internal/db keeps no other link.
var telegramPhotos = []string{"https://t.me", "https://*.telesco.pe"}

// setStaticHeaders sets what every response carries, whatever the handler.
//
//   - nosniff: a body is the type its Content-Type says.
//   - Referrer-Policy no-referrer everywhere, not only on pages with a token
//     in the URL: nothing on the site needs a Referer, and a callback with a
//     code in its query must never leak it.
//   - HSTS: Cloudflare ends TLS, so the origin sees plain HTTP, but the
//     header travels back to the browser. A year, no includeSubDomains (the
//     parent domain is not framy's) and no preload.
//   - COOP same-origin: the sign-in is a redirect, not a popup. It has no
//     effect inside the Telegram Web iframe, which is not a top-level page.
func setStaticHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

// InlineHash is the CSP source for an inline script or style whose text is
// exactly body, the bytes between the tags. Pass it in Config.CSPExtra under
// script-src or style-src.
//
// The policy has no nonce on purpose. A nonce must be new for every response,
// so the page would be generated per request and could never be cached with
// an ETag; the frontend is plain files, with scripts and styles loaded from
// the site's own /assets/, which 'self' already covers. The few inline blocks
// that remain are fixed text, which a hash pins just as well.
func InlineHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// cspDirective is one directive of the policy, with the sources it always
// has and whether the R2 origin joins them.
type cspDirective struct {
	name    string
	sources []string
	r2      bool
	// extendable: Config.CSPExtra may add to it.
	extendable bool
}

// cspDirectives is the policy, in the order it is written. default-src is
// 'none', so a type of content that is not listed is refused.
var cspDirectives = []cspDirective{
	{name: "default-src", sources: []string{"'none'"}},
	{name: "script-src", sources: []string{"'self'", telegramScript}, extendable: true},
	{name: "style-src", sources: []string{"'self'"}, extendable: true},
	// blob: and data: are for a picture or clip the person has just picked,
	// shown before it is uploaded.
	{name: "img-src", sources: append([]string{"'self'", "data:", "blob:"}, telegramPhotos...), r2: true, extendable: true},
	{name: "media-src", sources: []string{"'self'", "blob:"}, r2: true, extendable: true},
	{name: "connect-src", sources: []string{"'self'"}, r2: true, extendable: true},
	{name: "font-src", sources: []string{"'self'"}, extendable: true},
	{name: "base-uri", sources: []string{"'none'"}},
	{name: "object-src", sources: []string{"'none'"}},
	{name: "form-action", sources: []string{"'self'"}},
	{name: "frame-ancestors", sources: telegramWeb},
}

// buildCSP writes the one policy every HTML response carries. extra adds
// sources to the directives marked extendable; any other name, or a source
// that could end its directive or start another, is an error, so a typo
// fails at start instead of leaving a page without its script.
func buildCSP(r2Endpoint string, extra map[string][]string) (string, error) {
	r2, err := originOf(r2Endpoint)
	if err != nil {
		return "", err
	}
	for name, sources := range extra {
		i := slices.IndexFunc(cspDirectives, func(d cspDirective) bool { return d.name == name })
		if i < 0 || !cspDirectives[i].extendable {
			return "", fmt.Errorf("web: the CSP directive %q cannot be extended", name)
		}
		for _, src := range sources {
			if src == "" || strings.ContainsAny(src, ";, \t\r\n") {
				return "", fmt.Errorf("web: %q is not a CSP source", src)
			}
		}
	}
	parts := make([]string, 0, len(cspDirectives))
	for _, d := range cspDirectives {
		sources := slices.Clone(d.sources)
		if d.r2 && r2 != "" {
			sources = append(sources, r2)
		}
		sources = append(sources, extra[d.name]...)
		parts = append(parts, d.name+" "+strings.Join(slices.Compact(sources), " "))
	}
	return strings.Join(parts, "; "), nil
}

// originOf is scheme://host[:port] of an endpoint URL, or "" for none.
func originOf(endpoint string) (string, error) {
	if endpoint == "" {
		return "", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("web: the R2 endpoint must be an http or https URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

// isDocument reports whether a response is a page a browser would run
// scripts in, and so needs the CSP: HTML, XHTML and SVG. A response that
// names no type is counted in, since net/http will sniff one from the body
// and may find HTML.
func isDocument(contentType string) bool {
	if contentType == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return true
	}
	switch mt {
	case "text/html", "application/xhtml+xml", "image/svg+xml":
		return true
	}
	return false
}
