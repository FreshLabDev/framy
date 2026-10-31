// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// kekSize is the length of a key-encryption key: AES-256.
const kekSize = 32

// maxKEKVersion keeps a version well inside the INTEGER column.
const maxKEKVersion = 1_000_000

// KEK is the set of key-encryption keys read from the KEK file, each under a
// version number. The highest version wraps new data keys; the older ones stay
// in the file for as long as a row is still wrapped under them, so a rotation
// is: add a line with the next version, start the service, which rewraps
// every data key (Service.Rotate), then remove the old line.
//
// The file is text, one key a line, the base64 of 32 random bytes after the
// version and a colon. Blank lines and lines starting with # are ignored:
//
//	# framy KEK
//	1:3q2+7w8Qm4k1c0b9... (44 characters)
//	2:Zr1M0uXk5oV... (44 characters)
//
// A key is made with: printf '%s:%s\n' 1 "$(head -c 32 /dev/urandom | base64)"
type KEK struct {
	keys    map[int][]byte
	current int
	loose   bool
}

// ParseKEK reads the file's content. An error names a line and what is wrong
// with it, never the line itself.
func ParseKEK(data []byte) (*KEK, error) {
	k := &KEK{keys: map[int][]byte{}}
	for n, line := range bytes.Split(data, []byte("\n")) {
		text := strings.TrimSpace(string(line))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		versionText, keyText, ok := strings.Cut(text, ":")
		if !ok {
			return nil, fmt.Errorf("KEK file line %d: want <version>:<base64 key>", n+1)
		}
		version, err := strconv.Atoi(strings.TrimSpace(versionText))
		if err != nil || version < 1 || version > maxKEKVersion {
			return nil, fmt.Errorf("KEK file line %d: the version must be a whole number from 1 to %d", n+1, maxKEKVersion)
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyText))
		if err != nil {
			return nil, fmt.Errorf("KEK file line %d: the key is not base64", n+1)
		}
		if len(key) != kekSize {
			return nil, fmt.Errorf("KEK file line %d: the key must be %d bytes, it is %d", n+1, kekSize, len(key))
		}
		if _, dup := k.keys[version]; dup {
			return nil, fmt.Errorf("KEK file line %d: version %d is there twice", n+1, version)
		}
		k.keys[version] = key
		k.current = max(k.current, version)
	}
	if len(k.keys) == 0 {
		return nil, fmt.Errorf("the KEK file holds no key")
	}
	return k, nil
}

// LoadKEKFile reads the KEK file at path.
func LoadKEKFile(path string) (*KEK, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read the KEK file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the KEK file: %w", err)
	}
	defer clear(data)
	k, err := ParseKEK(data)
	if err != nil {
		return nil, err
	}
	k.loose = info.Mode().Perm()&0o077 != 0
	return k, nil
}

// Current is the version that wraps new data keys.
func (k *KEK) Current() int { return k.current }

// Versions lists the versions the file holds, ascending.
func (k *KEK) Versions() []int {
	out := make([]int, 0, len(k.keys))
	for v := range k.keys {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// Loose: the file let group or others read it when it was loaded. The
// design asks for mode 0400.
func (k *KEK) Loose() bool { return k.loose }

// Format keeps the key bytes out of any %v, %+v or %#v: a KEK in a log line
// or an error shows its versions and nothing else.
func (k *KEK) Format(f fmt.State, _ rune) {
	if k == nil {
		_, _ = fmt.Fprint(f, "keys.KEK(nil)")
		return
	}
	_, _ = fmt.Fprintf(f, "keys.KEK{versions: %v, current: %d}", k.Versions(), k.current)
}
