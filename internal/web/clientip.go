// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"net/netip"
	"strings"
)

// Who is the visitor.
//
// Behind the Cloudflare Tunnel every connection to the service comes from
// cloudflared, so the TCP peer is always the tunnel's address and says
// nothing about the visitor. Cloudflare's edge puts the visitor's address in
// CF-Connecting-IP and cloudflared passes it on, but any client that reaches
// the port can write that header too. So the header is believed only when the
// TCP peer is in TRUSTED_PROXIES, the addresses cloudflared connects from. A
// peer outside the list is taken at its word as the visitor, and its
// CF-Connecting-IP, X-Forwarded-For and Forwarded are ignored. Nothing but
// CF-Connecting-IP is ever read: X-Forwarded-For is a list the client can
// start, and True-Client-IP is an Enterprise header.
//
// What makes this safe is the network, not the header: the port is reachable
// only from cloudflared's Docker network, and TRUSTED_PROXIES names that
// network. If the list is empty, or the port is published to the host and the
// userland proxy makes every visitor look like the Docker gateway, the worst
// case is that everyone shares one bucket, never that someone can pick their
// own address.

type clientKey struct{}

// clientInfo is what the server knows about the other end of a request.
type clientInfo struct {
	// addr is the visitor's address: CF-Connecting-IP behind a trusted
	// proxy, the TCP peer otherwise. Invalid when the peer is not a TCP
	// address.
	addr netip.Addr
	// fromProxy: the TCP peer is a trusted proxy, so the other Cf-* headers
	// (CF-IPCountry) can be believed too.
	fromProxy bool
}

// ClientIP is the visitor's address as the server counted it, or the zero
// Addr outside a request that went through the Server.
func ClientIP(r *http.Request) netip.Addr {
	c, _ := r.Context().Value(clientKey{}).(clientInfo)
	return c.addr
}

// FromProxy reports whether the request came from a trusted proxy, which is
// when CF-IPCountry and the other headers Cloudflare adds are the edge's and
// not the visitor's.
func FromProxy(r *http.Request) bool {
	c, _ := r.Context().Value(clientKey{}).(clientInfo)
	return c.fromProxy
}

func (s *Server) client(r *http.Request) clientInfo {
	peer := peerAddr(r.RemoteAddr)
	if !s.trusted(peer) {
		s.noteUntrustedProxy(r, peer)
		return clientInfo{addr: peer}
	}
	info := clientInfo{addr: peer, fromProxy: true}
	// Exactly one value: a second one means something in between appended to
	// a header the edge had already set, and which of them is the visitor is
	// a guess.
	if vals := r.Header.Values("CF-Connecting-IP"); len(vals) == 1 {
		if a, err := netip.ParseAddr(strings.TrimSpace(vals[0])); err == nil {
			info.addr = a.Unmap().WithZone("")
		}
	}
	return info
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// noteUntrustedProxy names the setting to fix, once, when a request that has
// the edge's header comes from an address inside the host's own networks:
// that is cloudflared, and the list is wrong or empty.
func (s *Server) noteUntrustedProxy(r *http.Request, peer netip.Addr) {
	if r.Header.Get("CF-Connecting-IP") == "" || !(peer.IsPrivate() || peer.IsLoopback()) {
		return
	}
	s.proxyWarned.Do(func() {
		s.log.Warn("CF-Connecting-IP came from an address that is not in TRUSTED_PROXIES, so every visitor is counted as that address and shares one rate limit",
			"peer", peer.String())
	})
}

func peerAddr(remote string) netip.Addr {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap().WithZone("")
}

// bucketKey is what a limit counts a visitor by. An IPv6 visitor holds a
// whole /64 and can use any address in it, so the prefix is the key.
func bucketKey(a netip.Addr) string {
	switch {
	case !a.IsValid():
		return "?"
	case a.Is6():
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}
