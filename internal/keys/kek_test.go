// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func b64(fill byte, n int) string {
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = fill
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestParseKEK(t *testing.T) {
	file := "# framy KEK, made 2026-10-27\n\n1:" + b64(1, 32) + "\n  3 : " + b64(3, 32) + "  \r\n2:" + b64(2, 32)
	k, err := ParseKEK([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if k.Current() != 3 || fmt.Sprint(k.Versions()) != "[1 2 3]" {
		t.Errorf("current = %d, versions = %v", k.Current(), k.Versions())
	}
	if k.keys[2][0] != 2 {
		t.Error("a key was read into the wrong version")
	}
}

func TestParseKEKRefuses(t *testing.T) {
	secret := b64(7, 32)
	for name, file := range map[string]string{
		"nothing":            "",
		"only comments":      "# nothing here\n",
		"no version":         secret,
		"version zero":       "0:" + secret,
		"a negative version": "-1:" + secret,
		"a word as version":  "one:" + secret,
		"a huge version":     "99999999999:" + secret,
		"not base64":         "1:!!!" + secret,
		"a short key":        "1:" + b64(7, 16),
		"a long key":         "1:" + b64(7, 33),
		"an empty key":       "1:",
		"the same version":   "1:" + secret + "\n1:" + b64(8, 32),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseKEK([]byte(file))
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "BwcH") {
				t.Errorf("the error holds key text: %v", err)
			}
		})
	}
}

// A KEK in a log line or an error shows its versions and nothing else.
func TestKEKNeverPrintsItsKeys(t *testing.T) {
	k := testKEK(t, 1, 2)
	secrets := []string{base64.StdEncoding.EncodeToString(k.keys[1]), base64.StdEncoding.EncodeToString(k.keys[2])}
	wrapped := struct{ K *KEK }{k}
	var out strings.Builder
	for _, s := range []string{
		fmt.Sprintf("%v %+v %#v %s %d", k, k, k, k, k),
		fmt.Sprintf("%v %+v %#v", wrapped, wrapped, wrapped),
		fmt.Sprint(k),
	} {
		out.WriteString(s)
	}
	h := slog.NewTextHandler(&out, nil)
	slog.New(h).Info("kek", "kek", k)
	slog.New(slog.NewJSONHandler(&out, nil)).Info("kek", "kek", k)
	text := out.String()
	for _, s := range secrets {
		if strings.Contains(text, s) || strings.Contains(text, s[:16]) {
			t.Fatalf("a key was printed: %s", text)
		}
	}
	for _, b := range k.keys {
		if strings.Contains(text, fmt.Sprint(b[:8])) {
			t.Fatalf("a key was printed as bytes: %s", text)
		}
	}
	if !strings.Contains(text, "versions: [1 2]") {
		t.Errorf("the versions are not shown: %s", text)
	}
}

func TestLoadKEKFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kek")
	if err := os.WriteFile(path, []byte("1:"+b64(5, 32)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	k, err := LoadKEKFile(path)
	if err != nil || k.Current() != 1 || k.Loose() {
		t.Fatalf("load: %v, current %d, loose %v", err, k.Current(), k.Loose())
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if k, err = LoadKEKFile(path); err != nil || !k.Loose() {
		t.Errorf("a world-readable file is not reported: %v", err)
	}
	if _, err := LoadKEKFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file was accepted")
	}
	if err := os.WriteFile(path, []byte("1:short\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKEKFile(path); err == nil {
		t.Error("a bad file was accepted")
	}
}
