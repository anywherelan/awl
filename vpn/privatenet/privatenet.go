// Package privatenet lists the address ranges of a host's own network that an
// exit node keeps from its peers: LAN, CGNAT and link-local. The VPN gateway
// and the SOCKS5 exit node use the same list, so the two do not drift apart.
//
// The awl subnet itself is in 10.0.0.0/8 in practice, so traffic between awl
// peers through an exit node is dropped too, by design: peers reach each
// other directly via libp2p, not via an exit node.
package privatenet

import (
	"net/netip"
	"slices"
)

var prefixesV4 = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),  // RFC 6598 — CGNAT
	netip.MustParsePrefix("169.254.0.0/16"), // RFC 3927 — link-local
}

var prefixesV6 = []netip.Prefix{
	netip.MustParsePrefix("fc00::/7"),  // RFC 4193 — Unique Local Address (ULA), equivalent to RFC 1918 LANs
	netip.MustParsePrefix("fe80::/10"), // RFC 4291 — link-local, equivalent to 169.254.0.0/16
}

// PrefixesV4 returns the IPv4 ranges.
func PrefixesV4() []netip.Prefix {
	return slices.Clone(prefixesV4)
}

// PrefixesV6 returns the IPv6 ranges. LAN hosts with global IPv6 addresses
// are not covered: by prefix they cannot be told apart from the internet.
func PrefixesV6() []netip.Prefix {
	return slices.Clone(prefixesV6)
}

// Contains reports whether addr is in one of the ranges. An IPv4-mapped IPv6
// address counts as IPv4.
func Contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	prefixes := prefixesV6
	if addr.Is4() {
		prefixes = prefixesV4
	}
	return slices.ContainsFunc(prefixes, func(p netip.Prefix) bool {
		return p.Contains(addr)
	})
}
