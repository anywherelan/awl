package netstate

import (
	"net/netip"
	"slices"
)

// normalizeLANPeers returns a sorted copy of the awl addresses of the peers
// with LAN access, without duplicates and invalid addresses, IPv4-mapped IPv6
// addresses as IPv4. The managers compare it with what is applied, so a peers
// update that changes nothing does not touch the firewall.
func normalizeLANPeers(peers []netip.Addr) []netip.Addr {
	normalized := make([]netip.Addr, 0, len(peers))
	for _, peer := range peers {
		if peer.IsValid() {
			normalized = append(normalized, peer.Unmap())
		}
	}
	slices.SortFunc(normalized, netip.Addr.Compare)
	return slices.Compact(normalized)
}

// lanPeersOf returns the IPv6 addresses of peers if ipv6 is set, the IPv4 ones
// otherwise.
func lanPeersOf(peers []netip.Addr, ipv6 bool) []netip.Addr {
	return slices.DeleteFunc(slices.Clone(peers), func(peer netip.Addr) bool {
		return peer.Is6() != ipv6
	})
}
