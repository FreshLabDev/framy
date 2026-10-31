// SPDX-License-Identifier: Apache-2.0
package config

import (
	"net/netip"
	"strings"
	"testing"
)

func TestTrustedProxies(t *testing.T) {
	setBase(t)
	cfg, err := Load()
	if err != nil || len(cfg.Web.TrustedProxies) != 0 {
		t.Fatalf("default: %v, %v", cfg.Web.TrustedProxies, err)
	}
	t.Setenv("TRUSTED_PROXIES", " 172.20.0.0/16 , 10.1.2.3,fd00::/8, ::ffff:10.9.9.9 ,")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("172.20.0.0/16"),
		netip.MustParsePrefix("10.1.2.3/32"),
		netip.MustParsePrefix("fd00::/8"),
		netip.MustParsePrefix("10.9.9.9/32"),
	}
	if len(cfg.Web.TrustedProxies) != len(want) {
		t.Fatalf("proxies = %v", cfg.Web.TrustedProxies)
	}
	for i, p := range want {
		if cfg.Web.TrustedProxies[i] != p {
			t.Errorf("proxy %d = %v, want %v", i, cfg.Web.TrustedProxies[i], p)
		}
	}
	// A range with host bits set means its network.
	t.Setenv("TRUSTED_PROXIES", "172.20.5.9/16")
	if cfg, err = Load(); err != nil || cfg.Web.TrustedProxies[0] != netip.MustParsePrefix("172.20.0.0/16") {
		t.Errorf("unmasked range: %v, %v", cfg.Web.TrustedProxies, err)
	}
}

func TestTrustedProxiesRejects(t *testing.T) {
	for name, value := range map[string]string{
		"a name":            "cloudflared",
		"every IPv4":        "0.0.0.0/0",
		"every IPv6":        "::/0",
		"one good, one bad": "172.20.0.0/16,tunnel",
		"a bad length":      "172.20.0.0/40",
	} {
		t.Run(name, func(t *testing.T) {
			setBase(t)
			t.Setenv("TRUSTED_PROXIES", value)
			if _, err := Load(); err == nil {
				t.Fatalf("%q was accepted", value)
			}
		})
	}
}

func TestSiteURL(t *testing.T) {
	setBase(t)
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"https://framy.ws04.cc", "https://framy.ws04.cc"},
		{" https://framy.ws04.cc/ ", "https://framy.ws04.cc"},
		{"HTTPS://Framy.WS04.cc:8443", "https://framy.ws04.cc:8443"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080"},
		{"http://[::1]:8080", "http://[::1]:8080"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Setenv("SITE_URL", tc.in)
			cfg, err := Load()
			if err != nil || cfg.SiteURL != tc.want {
				t.Fatalf("SITE_URL=%q: %q, %v; want %q", tc.in, cfg.SiteURL, err, tc.want)
			}
		})
	}
	for _, bad := range []string{
		"framy.ws04.cc",                  // no scheme
		"http://framy.ws04.cc",           // OpenRouter takes https or localhost only
		"ftp://framy.ws04.cc",            // not a web origin
		"https://framy.ws04.cc/app",      // links are built by adding to it
		"https://framy.ws04.cc?x=1",      // a query
		"https://framy.ws04.cc/?",        // an empty query is still one
		"https://framy.ws04.cc#top",      // a fragment
		"https://user:pw@framy.ws04.cc",  // a login in it
		"https://",                       // no host
		"http://localhost.evil.example/", // looks like localhost
	} {
		t.Run("rejects "+bad, func(t *testing.T) {
			t.Setenv("SITE_URL", bad)
			_, err := Load()
			if err == nil {
				t.Fatalf("SITE_URL=%q was accepted", bad)
			}
			if strings.Contains(err.Error(), "pw@") {
				t.Error("the error names the login")
			}
		})
	}
}
