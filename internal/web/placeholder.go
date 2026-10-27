// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"time"
)

//go:embed placeholder.html
var placeholderHTML []byte

// Placeholder serves the page at / until the frontend replaces it: plain
// HTML with no script, style or outside asset, so it works under the CSP as
// it is and lets the headers be read with `curl -I` on the real domain.
// Register it as "GET /{$}".
func Placeholder() http.Handler {
	sum := sha256.Sum256(placeholderHTML)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(placeholderHTML))
	})
}
