// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// randomToken is 32 random bytes as unpadded base64url: 43 characters from
// the unreserved set, which is what RFC 7636 asks of a code verifier (43 to
// 128 characters) and is just as good for a state.
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// challenge is the S256 code challenge of a verifier: the unpadded base64url
// of its SHA-256 (RFC 7636, section 4.2). The plain method is never used: it
// sends the verifier itself through the browser.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// hashState is what the store keeps of a state, so a read of the table holds
// no value that the browser would need to present.
func hashState(state string) [sha256.Size]byte { return sha256.Sum256([]byte(state)) }
