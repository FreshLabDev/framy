// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// tunnelNet is where cloudflared connects from in these tests.
var tunnelNet = netip.MustParsePrefix("172.20.0.0/16")

func clientOf(t *testing.T, cfg Config, remote string, header map[string]string) (addr string, fromProxy bool) {
	t.Helper()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		addr, fromProxy = ClientIP(r).String(), FromProxy(r)
	})
	r := httptest.NewRequest(http.MethodGet, "/who", nil)
	r.RemoteAddr = remote
	for k, v := range header {
		r.Header.Set(k, v)
	}
	srv.ServeHTTP(httptest.NewRecorder(), r)
	return addr, fromProxy
}

func TestClientIPIsBelievedOnlyFromATrustedProxy(t *testing.T) {
	trusted := Config{TrustedProxies: []netip.Prefix{tunnelNet}}
	for _, tc := range []struct {
		name      string
		cfg       Config
		remote    string
		header    map[string]string
		wantAddr  string
		wantProxy bool
	}{
		{"through the tunnel", trusted, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "198.51.100.23"}, "198.51.100.23", true},
		{"through the tunnel, header in any case", trusted, "172.20.0.5:5555", map[string]string{"cf-connecting-ip": "198.51.100.23"}, "198.51.100.23", true},
		{"through the tunnel, IPv6 visitor", trusted, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "2001:db8::7"}, "2001:db8::7", true},
		{"through the tunnel, mapped IPv4", trusted, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "::ffff:198.51.100.23"}, "198.51.100.23", true},
		{"the tunnel with no header", trusted, "172.20.0.5:5555", nil, "172.20.0.5", true},
		{"the tunnel with a garbage header", trusted, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "not-an-ip"}, "172.20.0.5", true},
		{"the tunnel with a list", trusted, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "198.51.100.23, 10.0.0.1"}, "172.20.0.5", true},
		{"the tunnel with a zone", trusted, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "fe80::1%eth0"}, "fe80::1", true},

		// Someone who reaches the port directly, writing the header themselves.
		{"a stranger claims an address", trusted, "203.0.113.50:6000", map[string]string{"CF-Connecting-IP": "198.51.100.23"}, "203.0.113.50", false},
		{"a neighbour on the LAN claims an address", trusted, "192.168.1.20:6000", map[string]string{"CF-Connecting-IP": "198.51.100.23"}, "192.168.1.20", false},
		{"X-Forwarded-For is never read", trusted, "203.0.113.50:6000", map[string]string{"X-Forwarded-For": "198.51.100.23"}, "203.0.113.50", false},
		{"X-Forwarded-For through the tunnel is not read either", trusted, "172.20.0.5:5555", map[string]string{"X-Forwarded-For": "198.51.100.23"}, "172.20.0.5", true},
		{"True-Client-IP is not read", trusted, "172.20.0.5:5555", map[string]string{"True-Client-IP": "198.51.100.23"}, "172.20.0.5", true},
		{"Forwarded is not read", trusted, "203.0.113.50:6000", map[string]string{"Forwarded": "for=198.51.100.23"}, "203.0.113.50", false},

		// With no list, nobody is trusted, the tunnel included.
		{"no list, the tunnel", Config{}, "172.20.0.5:5555", map[string]string{"CF-Connecting-IP": "198.51.100.23"}, "172.20.0.5", false},
		{"no list, a stranger", Config{}, "203.0.113.50:6000", map[string]string{"CF-Connecting-IP": "198.51.100.23"}, "203.0.113.50", false},

		// The peer's own form.
		{"IPv6 peer", Config{}, "[2001:db8::9]:7000", nil, "2001:db8::9", false},
		{"mapped peer", Config{}, "[::ffff:203.0.113.50]:7000", nil, "203.0.113.50", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, proxy := clientOf(t, tc.cfg, tc.remote, tc.header)
			if addr != tc.wantAddr || proxy != tc.wantProxy {
				t.Fatalf("client = %s (proxy %v), want %s (proxy %v)", addr, proxy, tc.wantAddr, tc.wantProxy)
			}
		})
	}
}

func TestClientIPOutsideTheServerIsEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if ClientIP(r).IsValid() || FromProxy(r) {
		t.Fatal("a request that never went through the Server has a client")
	}
}

func TestBucketKey(t *testing.T) {
	for in, want := range map[string]string{
		"198.51.100.23":   "198.51.100.23",
		"2001:db8:1:2::1": "2001:db8:1:2::/64",
		"2001:db8:1:2::2": "2001:db8:1:2::/64",
		"2001:db8:1:3::1": "2001:db8:1:3::/64",
	} {
		if got := bucketKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("bucketKey(%s) = %s, want %s", in, got, want)
		}
	}
	if bucketKey(netip.Addr{}) != "?" {
		t.Error("an address that did not parse has no shared key")
	}
}

// A visitor who writes CF-Connecting-IP directly cannot pick the bucket they
// are counted in: rotating the header does not escape the limit.
func TestSpoofedHeaderDoesNotEscapeTheLimit(t *testing.T) {
	s := newSite(t, Config{
		TrustedProxies: []netip.Prefix{tunnelNet},
		Limits:         Limits{ClassDefault: {IP: perSecond(1, 3)}},
	})
	limited := 0
	for i := 0; i < 10; i++ {
		rr := s.do(t, req{
			target: "/page", remote: "203.0.113.50:6000",
			header: map[string]string{"CF-Connecting-IP": fmt.Sprintf("198.51.100.%d", i+1)},
		})
		if rr.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 7 {
		t.Fatalf("%d of 10 requests limited, want 7: the header chose the bucket", limited)
	}
}

// Through the tunnel the same header does separate visitors.
func TestTunnelVisitorsAreCountedApart(t *testing.T) {
	s := newSite(t, Config{
		TrustedProxies: []netip.Prefix{tunnelNet},
		Limits:         Limits{ClassDefault: {IP: perSecond(1, 2)}},
	})
	at := func(visitor string) int {
		return s.do(t, req{target: "/page", remote: "172.20.0.5:5555", header: map[string]string{"CF-Connecting-IP": visitor}}).Code
	}
	for i := 0; i < 2; i++ {
		if at("198.51.100.1") != 200 {
			t.Fatal("a visitor within the burst was refused")
		}
	}
	if at("198.51.100.1") != 429 {
		t.Error("a visitor over the burst was not refused")
	}
	if at("198.51.100.2") != 200 {
		t.Error("another visitor behind the same tunnel was counted with the first")
	}
}

func TestMisconfiguredProxyIsNamedOnce(t *testing.T) {
	var buf lockedBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	s := newSite(t, Config{Log: log})
	for i := 0; i < 3; i++ {
		s.do(t, req{target: "/page", remote: "172.20.0.5:5555", header: map[string]string{"CF-Connecting-IP": "198.51.100.1"}})
	}
	if n := strings.Count(buf.String(), "TRUSTED_PROXIES"); n != 1 {
		t.Fatalf("the warning appeared %d times: %s", n, buf.String())
	}
	// A stranger on a public address sending the header is not a proxy and
	// must not be able to fill the log.
	buf.Reset()
	s2 := newSite(t, Config{Log: log})
	s2.do(t, req{target: "/page", remote: "203.0.113.50:6000", header: map[string]string{"CF-Connecting-IP": "198.51.100.1"}})
	if buf.String() != "" {
		t.Fatalf("a public peer made a log line: %s", buf.String())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Reset()
}
