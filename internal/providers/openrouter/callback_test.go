// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"net/url"
	"strings"
	"testing"
)

func TestTheCallbackURLIsTheHooksPathWithTheJobAndItsMAC(t *testing.T) {
	c := testCallbacks()
	u, err := url.Parse(c.URL(7))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "framy.example" || u.RawQuery != "" || u.Fragment != "" {
		t.Errorf("url %s", u.Redacted())
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "hooks" || parts[1] != "or" || parts[2] != "7" || parts[3] != c.MAC(7) {
		t.Errorf("path segments %v", parts)
	}
	// 32 bytes, base64url with no padding: safe in a path as it is.
	if got := c.MAC(7); len(got) != 43 || strings.ContainsAny(got, "=+/") {
		t.Errorf("mac %q", got)
	}
}

func TestAPublicURLWithATrailingSlashMakesTheSameCallback(t *testing.T) {
	a, b := testCallbacks(), testCallbacks()
	b.PublicURL += "/"
	if a.URL(7) != b.URL(7) {
		t.Errorf("%q and %q", a.URL(7), b.URL(7))
	}
}

func TestOnlyTheMACOfThatJobAndThatSecretIsValid(t *testing.T) {
	c := testCallbacks()
	mac := c.MAC(7)

	flipped := []byte(mac)
	flipped[0] ^= 1
	// The last character of 32 bytes in base64 has two bits to spare; a
	// spelling that differs only there is the same bytes, and is refused.
	spare := []byte(mac)
	spare[len(spare)-1] ^= 1
	other := Callbacks{PublicURL: c.PublicURL, Secret: []byte(strings.Repeat("another-", 8))}

	for name, tc := range map[string]struct {
		cb   Callbacks
		id   int64
		mac  string
		want bool
	}{
		"the right one":             {c, 7, mac, true},
		"another job":               {c, 8, mac, false},
		"one character off":         {c, 7, string(flipped), false},
		"another spelling":          {c, 7, string(spare), false},
		"truncated":                 {c, 7, mac[:len(mac)-1], false},
		"longer":                    {c, 7, mac + "A", false},
		"empty":                     {c, 7, "", false},
		"padded":                    {c, 7, mac + "=", false},
		"not base64":                {c, 7, "!!!", false},
		"another secret":            {other, 7, mac, false},
		"the mac of another secret": {c, 7, other.MAC(7), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.cb.Valid(tc.id, tc.mac); got != tc.want {
				t.Errorf("Valid = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEveryJobHasItsOwnMAC(t *testing.T) {
	c := testCallbacks()
	seen := map[string]int64{}
	for id := int64(1); id <= 500; id++ {
		if prev, dup := seen[c.MAC(id)]; dup {
			t.Fatalf("jobs %d and %d share a mac", prev, id)
		}
		seen[c.MAC(id)] = id
	}
}

// Callbacks that are off accept no path, even one made with an empty secret.
func TestCallbacksThatAreOffAcceptNothing(t *testing.T) {
	for name, c := range map[string]Callbacks{
		"zero":       {},
		"no secret":  {PublicURL: "https://framy.example"},
		"plain http": {PublicURL: "http://framy.example", Secret: []byte("secret")},
	} {
		t.Run(name, func(t *testing.T) {
			if c.Enabled() {
				t.Error("enabled")
			}
			for _, mac := range []string{c.MAC(7), "", "AAAA"} {
				if c.Valid(7, mac) {
					t.Errorf("mac %q is valid with callbacks off", mac)
				}
			}
		})
	}
}
