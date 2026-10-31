// SPDX-License-Identifier: Apache-2.0

// Package safehttp fetches URLs someone else gave framy: a provider's link
// to a result, a person's link to a picture. It connects only to public
// addresses, checked on the address it dials after DNS answered, so a name
// that resolves to the host's own network is refused too. It speaks HTTPS
// on port 443 only and follows at most three redirects, each checked the
// same way.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrNotPublic: the URL leads to an address framy does not fetch from.
var ErrNotPublic = errors.New("not a public address")

// notPublic are ranges outside the private, loopback, link-local and
// multicast ones that are still not the internet.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// Public says whether framy may connect to ip.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// CheckURL refuses what is not an HTTPS URL on port 443 without
// credentials in it.
func CheckURL(u *url.URL) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return fmt.Errorf("%w: only https on port 443", ErrNotPublic)
	}
	return nil
}

// control runs after DNS, on the address about to be dialed.
func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !Public(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrNotPublic, address)
	}
	return nil
}

// Client returns a client whose every request, redirects included, goes
// to a public address only. timeout bounds the whole request.
func Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	transport := &http.Transport{
		// No proxy: a proxy would dial for us and skip the check.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &http.Client{
		Transport: &checked{next: transport},
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("more than three redirects")
			}
			return CheckURL(req.URL)
		},
	}
}

// checked refuses a URL before any dial.
type checked struct{ next http.RoundTripper }

func (c *checked) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckURL(req.URL); err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil && !Public(ip) {
		return nil, fmt.Errorf("%w: %s", ErrNotPublic, ip)
	}
	return c.next.RoundTrip(req)
}
