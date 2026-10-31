// SPDX-License-Identifier: Apache-2.0
package safehttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"1.1.1.1":                  true,
		"104.16.0.1":               true,
		"2606:4700::1111":          true,
		"10.0.0.1":                 false,
		"172.16.5.4":               false,
		"192.168.1.34":             false,
		"127.0.0.1":                false,
		"169.254.169.254":          false,
		"100.64.0.1":               false,
		"0.0.0.0":                  false,
		"224.0.0.1":                false,
		"255.255.255.255":          false,
		"::1":                      false,
		"fe80::1":                  false,
		"fd00::1":                  false,
		"::ffff:127.0.0.1":         false,
		"::ffff:192.168.1.1":       false,
		"64:ff9b::7f00:1":          false,
		"2002:7f00:1::":            false,
		"2001:db8::1":              false,
		"198.18.0.1":               false,
		"::":                       false,
		"203.0.113.9":              false,
		"192.0.2.1":                false,
		"240.0.0.1":                false,
		"::ffff:1.1.1.1":           true,
		"2a00:1450:4001:82b::200e": true,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/a.png":           true,
		"https://example.com:443/a.png":       true,
		"http://example.com/a.png":            false,
		"https://user:pass@example.com/a":     false,
		"https://example.com:8443/a":          false,
		"file:///etc/passwd":                  false,
		"gopher://example.com/":               false,
		"https:///nohost":                     false,
		"https://[::1]/a":                     true, // refused at the dial
		"https://169.254.169.254/latest/meta": true,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := CheckURL(u) == nil; got != ok {
			t.Errorf("CheckURL(%s) passes: %v, want %v", raw, got, ok)
		}
	}
}

func TestTheClientRefusesThisHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request reached the loopback server")
	}))
	defer srv.Close()
	c := Client(5 * time.Second)
	for _, target := range []string{
		srv.URL,
		// A name that resolves to the loopback is caught at the dial.
		"https://localhost:443/",
		"https://127.0.0.1/",
		"https://[::1]/",
		"http://example.com/",
	} {
		_, err := c.Get(target)
		if err == nil || !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: %v, want ErrNotPublic", target, err)
		}
	}
	// The dial check itself, for a name DNS answers with a private address.
	if err := control("tcp", "192.168.1.34:443", nil); !errors.Is(err, ErrNotPublic) {
		t.Errorf("control: %v", err)
	}
	if err := control("tcp", "1.1.1.1:443", nil); err != nil {
		t.Errorf("control on a public address: %v", err)
	}
	if err := control("tcp", "not-an-address", nil); err == nil || !strings.Contains(err.Error(), "not a public") {
		t.Errorf("control on garbage: %v", err)
	}
}

func TestARedirectToAPrivateAddressIsRefused(t *testing.T) {
	c := Client(5 * time.Second)
	req, _ := http.NewRequest(http.MethodGet, "http://10.0.0.1/", nil)
	if err := c.CheckRedirect(req, []*http.Request{{}}); err == nil {
		t.Error("a redirect to http is followed")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err := c.CheckRedirect(req, make([]*http.Request, 3)); err == nil {
		t.Error("a fourth redirect is followed")
	}
}
