package netstate

import (
	"fmt"
	"net/netip"
)

// privateSubnets is the destination set we refuse to forward from the gateway,
// so the exit node's LAN, link-local, and CGNAT space stay invisible to
// clients. awlSubnet itself is contained in 10.0.0.0/8 in practice, so
// awl↔awl forward through the gateway is also dropped here — by design:
// peers reach each other directly via libp2p, not via routed IP through an
// exit node.
//
// Shared across platforms: Linux feeds the strings to iptables as-is,
// Windows parses them into netip.Prefix for the WFP forward-layer filter.
var privateSubnets = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",  // RFC 6598 — CGNAT
	"169.254.0.0/16", // RFC 3927 — link-local
}

// privateSubnetsV6 covers the IPv6 equivalents of LAN and link-local space.
// Exit nodes must drop forwarding to these to prevent local IPv6 network exposure.
var privateSubnetsV6 = []string{
	"fc00::/7",  // RFC 4193 — Unique Local Address (ULA), equivalent to RFC 1918 LANs
	"fe80::/10", // RFC 4291 — Link-local address, equivalent to 169.254.0.0/16
}

// privateSubnetPrefixes returns privateSubnets parsed into netip.Prefix.
// Panics on a malformed entry — the list is a compile-time constant and is
// verified by a unit test, so a panic here means a broken edit, not runtime
// input.
func privateSubnetPrefixes() []netip.Prefix {
	return parsePrefixList(privateSubnets, "privateSubnets")
}

// privateSubnetPrefixesV6 returns privateSubnetsV6 parsed into netip.Prefix.
// Panics on a malformed entry for the same reasons as privateSubnetPrefixes.
func privateSubnetPrefixesV6() []netip.Prefix {
	return parsePrefixList(privateSubnetsV6, "privateSubnetsV6")
}

// AllPrivateSubnetPrefixes returns a combined slice of both IPv4 and IPv6 private prefixes.
// Useful for cross-platform engines (like Windows WFP or Go-native packet matchers)
// that handle both IP families in a single pass.
func AllPrivateSubnetPrefixes() []netip.Prefix {
	v4 := privateSubnetPrefixes()
	v6 := privateSubnetPrefixesV6()
	return append(v4, v6...)
}

func parsePrefixList(subnets []string, name string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(subnets))
	for _, s := range subnets {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic(fmt.Sprintf("%s contains malformed prefix %q: %v", name, s, err))
		}
		prefixes = append(prefixes, p)
	}
	return prefixes
}
