// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Web is what the public listener (internal/web) is told about the network.
type Web struct {
	// TrustedProxies are the addresses cloudflared connects from. Only a
	// request from one of them is believed when it names the visitor in
	// CF-Connecting-IP. Empty trusts no one.
	TrustedProxies []netip.Prefix
}

// loadWeb reads TRUSTED_PROXIES: addresses and CIDR ranges separated by
// commas. A range that holds every address is refused, since it would let any
// client name its own address.
func loadWeb() (Web, error) {
	var w Web
	for _, item := range strings.Split(valueOrDefault("TRUSTED_PROXIES", ""), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil {
			a, addrErr := netip.ParseAddr(item)
			if addrErr != nil {
				return Web{}, fmt.Errorf("TRUSTED_PROXIES: %q is neither an address nor a CIDR range", item)
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Bits() == 0 {
			return Web{}, fmt.Errorf("TRUSTED_PROXIES: %q would trust every address", item)
		}
		w.TrustedProxies = append(w.TrustedProxies, p.Masked())
	}
	return w, nil
}
