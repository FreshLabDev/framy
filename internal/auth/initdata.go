// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/tg"
)

// MaxInitDataAge is how old a Mini App's auth_date may be. Telegram signs
// initData once, when the Mini App opens, and never refreshes it, so this is
// also how long after opening a Mini App can still ask for a token.
const MaxInitDataAge = time.Hour

// initDataSkew tolerates an auth_date slightly ahead of this server's clock.
const initDataSkew = time.Minute

var (
	// ErrBadSignature: the hash does not match what was signed, or there is
	// none. The data was forged, edited, or made for another bot.
	ErrBadSignature = errors.New("initData signature does not match")
	// ErrStale: the signature is right and auth_date is older than
	// MaxInitDataAge, or from the future.
	ErrStale = errors.New("initData is too old")
	// ErrMalformed: the text is not initData.
	ErrMalformed = errors.New("initData is malformed")
)

// InitData is what a verified Mini App launch says.
type InitData struct {
	User     tg.User
	AuthDate time.Time
	// StartParam is the signed startapp value of the link that opened the Mini
	// App, if any.
	StartParam string
}

// Verifier checks the initData of a Mini App against the bot's token, the way
// maply/internal/auth does: the data-check-string is every field but hash,
// sorted by key, as key=value lines, and the key is
// HMAC-SHA256("WebAppData", token).
type Verifier struct {
	secret []byte
	now    func() time.Time
}

// NewVerifier makes the verifier of a bot's Mini App. now may be nil.
func NewVerifier(botToken string, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	return &Verifier{secret: hmacSHA256([]byte("WebAppData"), []byte(botToken)), now: now}
}

// Verify checks the hash of a raw initData query string, then its age, and
// returns what it says. Nothing in raw is believed before the hash matches.
func (v *Verifier) Verify(raw string) (InitData, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return InitData{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	keys := make([]string, 0, len(values))
	for k, vs := range values {
		// A repeated field would let two readers disagree on what was signed.
		if len(vs) != 1 {
			return InitData{}, fmt.Errorf("%w: field %q appears %d times", ErrMalformed, k, len(vs))
		}
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	got, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(got) != sha256.Size {
		return InitData{}, ErrBadSignature
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + values.Get(k)
	}
	if !hmac.Equal(got, hmacSHA256(v.secret, []byte(strings.Join(lines, "\n")))) {
		return InitData{}, ErrBadSignature
	}

	// Only signed data is read past this point.
	unix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return InitData{}, fmt.Errorf("%w: auth_date", ErrMalformed)
	}
	authDate := time.Unix(unix, 0)
	now := v.now()
	if now.Sub(authDate) > MaxInitDataAge || authDate.Sub(now) > initDataSkew {
		return InitData{}, ErrStale
	}
	var user tg.User
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return InitData{}, fmt.Errorf("%w: user", ErrMalformed)
	}
	return InitData{User: user, AuthDate: authDate, StartParam: values.Get("start_param")}, nil
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
