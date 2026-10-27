// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// testKEK builds a KEK file's content from fresh random keys, one per version,
// or from the given bytes.
func testKEK(t *testing.T, versions ...int) *KEK {
	t.Helper()
	var file strings.Builder
	for _, v := range versions {
		raw := make([]byte, kekSize)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&file, "%d:%s\n", v, base64.StdEncoding.EncodeToString(raw))
	}
	k, err := ParseKEK([]byte(file.String()))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// kekOf is the KEK with only the given versions of k's keys.
func kekOf(k *KEK, versions ...int) *KEK {
	out := &KEK{keys: map[int][]byte{}}
	for _, v := range versions {
		out.keys[v] = k.keys[v]
		out.current = max(out.current, v)
	}
	return out
}

var (
	bound = binding{userID: 4242, provider: "openai", connectionID: "6f1f2c5e-8a40-4c89-9d1f-0a8f1f3a2b11"}
	// plain is the key the tests hide; it is long enough to spot in bytes.
	plain = "sk-proj-Zq81nT3vLw5RkE7yUo2Pd9Hs4Bj6Xc0MaGfIt"
)

func TestSealRoundTrips(t *testing.T) {
	k := testKEK(t, 1)
	e, err := k.seal(bound, []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if e.kekVersion != 1 {
		t.Errorf("kek version = %d", e.kekVersion)
	}
	got, err := k.open(bound, e)
	if err != nil || string(got) != plain {
		t.Fatalf("open = %q, %v", got, err)
	}
	for name, field := range map[string][]byte{"ciphertext": e.ciphertext, "wrapped data key": e.wrappedDEK} {
		if bytes.Contains(field, []byte(plain)) || bytes.Contains(field, []byte(plain[10:30])) {
			t.Errorf("the %s holds the key in the clear", name)
		}
	}
	// 12 bytes of nonce, the text, 16 of tag; and 32 bytes of data key.
	if len(e.ciphertext) != 12+len(plain)+16 || len(e.wrappedDEK) != 12+kekSize+16 {
		t.Errorf("sizes = %d, %d", len(e.ciphertext), len(e.wrappedDEK))
	}
}

// Every seal is a new data key and a new nonce: the same key sealed twice
// shares nothing.
func TestSealIsNewEachTime(t *testing.T) {
	k := testKEK(t, 1)
	a, _ := k.seal(bound, []byte(plain))
	b, _ := k.seal(bound, []byte(plain))
	if bytes.Equal(a.ciphertext, b.ciphertext) || bytes.Equal(a.wrappedDEK, b.wrappedDEK) {
		t.Fatal("two seals of one key are the same bytes")
	}
	dekA, _ := k.unwrap(bound, a)
	dekB, _ := k.unwrap(bound, b)
	if bytes.Equal(dekA, dekB) {
		t.Fatal("two records share a data key")
	}
}

// What the envelope is bound to: change any part of it and the key does not
// open.
func TestTheAssociatedDataIsBinding(t *testing.T) {
	// Versions 1 and 2 hold the same key bytes, so the version in the
	// associated data is the only thing that tells a wrap under 1 from one
	// under 2.
	k := testKEK(t, 1)
	k.keys[2] = k.keys[1]
	k.current = 1
	e, err := k.seal(bound, []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		b binding
		e envelope
	}{
		"another person":      {binding{userID: bound.userID + 1, provider: bound.provider, connectionID: bound.connectionID}, e},
		"another provider":    {binding{userID: bound.userID, provider: "gemini", connectionID: bound.connectionID}, e},
		"another connection":  {binding{userID: bound.userID, provider: bound.provider, connectionID: "6f1f2c5e-8a40-4c89-9d1f-0a8f1f3a2b12"}, e},
		"another KEK version": {bound, envelope{ciphertext: e.ciphertext, wrappedDEK: e.wrappedDEK, kekVersion: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := k.open(tc.b, tc.e)
			if err == nil || got != nil {
				t.Fatalf("opened as %s: %q", name, got)
			}
		})
	}
	if got, err := k.open(bound, e); err != nil || string(got) != plain {
		t.Fatalf("the control does not open: %v", err)
	}
}

// The key and its data key are bound separately: a wrap from one record
// does not open another record's key, nor does a key move under a wrap that
// belongs to a different record of the same person.
func TestRecordsDoNotMix(t *testing.T) {
	k := testKEK(t, 1)
	a, _ := k.seal(bound, []byte(plain))
	other := binding{userID: bound.userID, provider: "gemini", connectionID: "11111111-2222-4333-8444-555555555555"}
	b, _ := k.seal(other, []byte("AIza"+strings.Repeat("Qw1", 12)))
	for name, tc := range map[string]struct {
		b binding
		e envelope
	}{
		"a's key under b's wrap":  {bound, envelope{ciphertext: a.ciphertext, wrappedDEK: b.wrappedDEK, kekVersion: 1}},
		"b's key under a's wrap":  {other, envelope{ciphertext: b.ciphertext, wrappedDEK: a.wrappedDEK, kekVersion: 1}},
		"a's whole record as b":   {other, a},
		"b's key as a's record":   {bound, b},
		"a's key with b's wrap/b": {other, envelope{ciphertext: a.ciphertext, wrappedDEK: b.wrappedDEK, kekVersion: 1}},
	} {
		if got, err := k.open(tc.b, tc.e); err == nil {
			t.Errorf("%s opened: %q", name, got)
		}
	}
}

func TestATamperedEnvelopeDoesNotOpen(t *testing.T) {
	k := testKEK(t, 1)
	e, _ := k.seal(bound, []byte(plain))
	for name, tamper := range map[string]func(envelope) envelope{
		"a bit of the ciphertext": func(e envelope) envelope { e.ciphertext = flip(e.ciphertext, 20); return e },
		"the ciphertext's nonce":  func(e envelope) envelope { e.ciphertext = flip(e.ciphertext, 0); return e },
		"the ciphertext's tag":    func(e envelope) envelope { e.ciphertext = flip(e.ciphertext, len(e.ciphertext)-1); return e },
		"a bit of the wrap":       func(e envelope) envelope { e.wrappedDEK = flip(e.wrappedDEK, 20); return e },
		"a cut ciphertext":        func(e envelope) envelope { e.ciphertext = e.ciphertext[:len(e.ciphertext)-1]; return e },
		"an empty ciphertext":     func(e envelope) envelope { e.ciphertext = nil; return e },
		"an empty wrap":           func(e envelope) envelope { e.wrappedDEK = nil; return e },
	} {
		if got, err := k.open(bound, tamper(e)); err == nil {
			t.Errorf("opened with %s changed: %q", name, got)
		}
	}
}

func flip(b []byte, i int) []byte {
	out := bytes.Clone(b)
	out[i] ^= 1
	return out
}

// The point of the wrap: a rotation moves the data key to the new KEK and
// leaves the encrypted key where it is.
func TestRewrapMovesTheDataKeyAndNothingElse(t *testing.T) {
	both := testKEK(t, 1, 2)
	oldOnly := kekOf(both, 1)
	e, err := oldOnly.seal(bound, []byte(plain))
	if err != nil || e.kekVersion != 1 {
		t.Fatalf("seal: %v, version %d", err, e.kekVersion)
	}
	moved, err := both.rewrap(bound, e)
	if err != nil {
		t.Fatal(err)
	}
	if moved.kekVersion != 2 {
		t.Errorf("version = %d, want 2", moved.kekVersion)
	}
	if !bytes.Equal(moved.ciphertext, e.ciphertext) {
		t.Error("the encrypted key changed: the rotation re-encrypted it")
	}
	if bytes.Equal(moved.wrappedDEK, e.wrappedDEK) {
		t.Error("the wrap did not change")
	}
	newOnly := kekOf(both, 2)
	if got, err := newOnly.open(bound, moved); err != nil || string(got) != plain {
		t.Fatalf("the new KEK alone cannot open it: %v", err)
	}
	if _, err := oldOnly.open(bound, moved); err == nil {
		t.Error("the old KEK still opens the rewrapped key")
	}
	// The old line can go once nothing is wrapped under it.
	if _, err := newOnly.rewrap(bound, e); err == nil {
		t.Error("rewrapped a record whose KEK version is not in the file")
	}
}

func TestRewrapKeepsTheBinding(t *testing.T) {
	both := testKEK(t, 1, 2)
	e, _ := kekOf(both, 1).seal(bound, []byte(plain))
	if _, err := both.rewrap(binding{userID: bound.userID + 1, provider: bound.provider, connectionID: bound.connectionID}, e); err == nil {
		t.Error("rewrapped a data key for another person")
	}
	moved, err := both.rewrap(bound, e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := both.open(binding{userID: bound.userID + 1, provider: bound.provider, connectionID: bound.connectionID}, moved); err == nil {
		t.Error("the rewrapped record opens for another person")
	}
}

func TestSealRefusesAnAmbiguousBinding(t *testing.T) {
	k := testKEK(t, 1)
	for _, b := range []binding{
		{userID: 1, provider: "open|ai", connectionID: "x"},
		{userID: 1, provider: "openai", connectionID: "a|b"},
		{userID: 1, provider: "", connectionID: "x"},
		{userID: 1, provider: "openai", connectionID: ""},
	} {
		if _, err := k.seal(b, []byte(plain)); err == nil {
			t.Errorf("sealed for %+v", b)
		}
	}
}

// An error from the envelope names no key and no associated data.
func TestEnvelopeErrorsCarryNoKey(t *testing.T) {
	k := testKEK(t, 1)
	e, _ := k.seal(bound, []byte(plain))
	_, err := k.open(binding{userID: 1, provider: "openai", connectionID: "x"}, e)
	if err == nil {
		t.Fatal("opened")
	}
	for _, secret := range []string{plain, plain[:10], base64.StdEncoding.EncodeToString(k.keys[1])} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error holds %q", secret)
		}
	}
}
