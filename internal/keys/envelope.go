// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// An envelope is a key at rest. Two AES-256-GCM layers, both through
// cipher.NewGCMWithRandomNonce, so each value is nonce, ciphertext and tag in
// one:
//
//	ciphertext  = Seal(dataKey, key,     ad: user | provider | connection)
//	wrappedDEK  = Seal(KEK[v],  dataKey, ad: user | provider | connection | v)
//
// The data key is new for every record. The KEK version is bound where the
// KEK is used, in the wrap. It is not bound in the layer the data key seals:
// that value would then have to be encrypted again for each rotation, and
// the rotation is meant to rewrap data keys only. A key therefore cannot be
// moved to another person, provider or connection, and a data key cannot be
// presented under another KEK version.
type envelope struct {
	ciphertext []byte
	wrappedDEK []byte
	kekVersion int
}

// binding is what an envelope is tied to.
type binding struct {
	userID       int64
	provider     string
	connectionID string
}

// errSeal and errOpen carry no detail on purpose: the cause of an
// authentication failure is nothing a log needs, and nothing here may echo a
// key or the associated data.
var (
	errSeal = errors.New("keys: cannot seal")
	errOpen = errors.New("keys: cannot open")
)

// valid refuses a field that could blur the separator of the associated data.
func (b binding) valid() bool {
	return b.provider != "" && b.connectionID != "" &&
		!strings.Contains(b.provider, "|") && !strings.Contains(b.connectionID, "|")
}

func (b binding) keyAD() []byte {
	return []byte("framy/key/v1|" + strconv.FormatInt(b.userID, 10) + "|" + b.provider + "|" + b.connectionID)
}

func (b binding) wrapAD(kekVersion int) []byte {
	return []byte("framy/dek/v1|" + strconv.FormatInt(b.userID, 10) + "|" + b.provider + "|" + b.connectionID + "|" + strconv.Itoa(kekVersion))
}

func sealAES(key, plaintext, ad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errSeal
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, errSeal
	}
	return aead.Seal(nil, nil, plaintext, ad), nil
}

func openAES(key, sealed, ad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errOpen
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, errOpen
	}
	plaintext, err := aead.Open(nil, nil, sealed, ad)
	if err != nil {
		return nil, errOpen
	}
	return plaintext, nil
}

// seal encrypts plaintext under a new data key and wraps that under the
// current KEK.
func (k *KEK) seal(b binding, plaintext []byte) (envelope, error) {
	if !b.valid() {
		return envelope{}, errSeal
	}
	dek := make([]byte, kekSize)
	if _, err := rand.Read(dek); err != nil {
		return envelope{}, errSeal
	}
	defer clear(dek)
	ciphertext, err := sealAES(dek, plaintext, b.keyAD())
	if err != nil {
		return envelope{}, err
	}
	wrapped, err := sealAES(k.keys[k.current], dek, b.wrapAD(k.current))
	if err != nil {
		return envelope{}, err
	}
	return envelope{ciphertext: ciphertext, wrappedDEK: wrapped, kekVersion: k.current}, nil
}

// unwrap returns the data key of e. The caller clears it.
func (k *KEK) unwrap(b binding, e envelope) ([]byte, error) {
	kek, ok := k.keys[e.kekVersion]
	if !ok || !b.valid() {
		return nil, errOpen
	}
	return openAES(kek, e.wrappedDEK, b.wrapAD(e.kekVersion))
}

// open decrypts the key. The caller owns the result.
func (k *KEK) open(b binding, e envelope) ([]byte, error) {
	dek, err := k.unwrap(b, e)
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	return openAES(dek, e.ciphertext, b.keyAD())
}

// rewrap moves the data key of e to the current KEK. The ciphertext is not
// touched: the same bytes stay in the row. The result is opened once before
// it is returned, so a row never gets a wrap that cannot be read.
func (k *KEK) rewrap(b binding, e envelope) (envelope, error) {
	dek, err := k.unwrap(b, e)
	if err != nil {
		return envelope{}, err
	}
	defer clear(dek)
	wrapped, err := sealAES(k.keys[k.current], dek, b.wrapAD(k.current))
	if err != nil {
		return envelope{}, err
	}
	out := envelope{ciphertext: e.ciphertext, wrappedDEK: wrapped, kekVersion: k.current}
	plaintext, err := k.open(b, out)
	if err != nil {
		return envelope{}, fmt.Errorf("%w: the rewrapped key does not read back", errSeal)
	}
	clear(plaintext)
	return out, nil
}
