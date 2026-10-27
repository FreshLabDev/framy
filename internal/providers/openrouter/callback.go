// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
)

// hookPrefix is where OpenRouter's webhooks arrive: /hooks/or/<job>/<mac>.
const hookPrefix = "/hooks/or/"

// Callbacks makes the callback_url of a video request and checks the path of
// a webhook that comes back. The path ends in an HMAC of the job's id under a
// secret that only framy has, so nobody who has not seen the URL can make
// framy look at a job. It is a capability, not a signature: anyone who read
// the URL (OpenRouter's logs, a person's own account) can post to it, which
// is why a webhook only ever brings a poll forward.
//
// The zero value is off: a video request then carries no callback_url, and
// the 30 s poll finishes the run on its own.
type Callbacks struct {
	// PublicURL is the origin OpenRouter reaches framy at, such as
	// https://framy.ws04.cc. OpenRouter takes a callback_url over HTTPS only.
	PublicURL string
	// Secret keys the MAC. Rotating it ends the callbacks of runs in flight,
	// which fall back to polling.
	Secret []byte
}

// Enabled: requests carry a callback_url and the hook accepts paths.
func (c Callbacks) Enabled() bool {
	return strings.HasPrefix(c.PublicURL, "https://") && len(c.Secret) > 0
}

// URL is the callback_url of job id.
func (c Callbacks) URL(id int64) string {
	return strings.TrimRight(c.PublicURL, "/") + hookPrefix + strconv.FormatInt(id, 10) + "/" + c.MAC(id)
}

// MAC is the last segment of the job's callback path.
func (c Callbacks) MAC(id int64) string {
	return base64.RawURLEncoding.EncodeToString(c.mac(id))
}

func (c Callbacks) mac(id int64) []byte {
	m := hmac.New(sha256.New, c.Secret)
	m.Write([]byte("framy/hooks/openrouter/v1:" + strconv.FormatInt(id, 10)))
	return m.Sum(nil)
}

// Valid says whether mac is the one of job id. It compares in constant time,
// and takes only the one spelling of it.
func (c Callbacks) Valid(id int64, mac string) bool {
	if !c.Enabled() {
		return false
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(mac)
	return err == nil && hmac.Equal(got, c.mac(id))
}
