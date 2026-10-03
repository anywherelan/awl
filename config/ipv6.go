package config

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"

	"github.com/libp2p/go-libp2p/core/peer"
)

// maxAddrAttempts bounds the search for a free address in pickPeerAddr. With
// the prefix lengths we accept it is never reached; it only keeps the loop
// finite.
const maxAddrAttempts = 1 << 16

// deriveAddr deterministically maps a peer ID into prefix: the network bits
// come from prefix, the host bits from SHA-256([]byte(id)).
//
// Hash bits are taken at the same positions they occupy in the address (byte i
// of the address from byte i of the hash), not from the start of the hash. So
// the low bits of the result do not depend on the prefix length: for any IPv6
// prefix up to /64 the interface identifier is the same.
//
// The result is only a starting point: it may collide with another peer or be
// a reserved address (e.g. the all-zero host), see pickPeerAddr. Works for both
// IPv4 and IPv6 prefixes.
func deriveAddr(id peer.ID, prefix netip.Prefix) netip.Addr {
	sum := sha256.Sum256([]byte(id))
	addr := prefix.Masked().Addr().AsSlice()
	mask := net.CIDRMask(prefix.Bits(), len(addr)*8)
	for i := range addr {
		addr[i] |= sum[i] &^ mask[i]
	}

	res, _ := netip.AddrFromSlice(addr)
	return res
}

// pickPeerAddr chooses our view of a peer's address inside prefix. The address
// the peer announced is taken if check accepts it. Otherwise rejectErr says why
// it was not, and the first address accepted by check is taken, starting from
// deriveAddr(id) and counting up. err is set only when no free address is found.
func pickPeerAddr(id peer.ID, prefix netip.Prefix, announced string, check func(netip.Addr) error) (addr netip.Addr, rejectErr, err error) {
	announcedAddr, rejectErr := netip.ParseAddr(announced)
	if rejectErr == nil {
		rejectErr = check(announcedAddr)
		if rejectErr == nil {
			return announcedAddr, nil, nil
		}
	}

	candidate := deriveAddr(id, prefix)
	for range maxAddrAttempts {
		if check(candidate) == nil {
			return candidate, rejectErr, nil
		}
		candidate = nextAddr(candidate, prefix)
	}

	return netip.Addr{}, rejectErr, fmt.Errorf("no free address in %s", prefix.Masked())
}

// nextAddr returns the address following a, wrapping around to the first
// address of prefix.
func nextAddr(a netip.Addr, prefix netip.Prefix) netip.Addr {
	next := a.Next()
	if !next.IsValid() || !prefix.Contains(next) {
		return prefix.Masked().Addr()
	}
	return next
}
