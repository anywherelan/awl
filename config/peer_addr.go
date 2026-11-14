package config

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"

	"github.com/libp2p/go-libp2p/core/peer"
)

// The awl addresses of IPv4 and IPv6 work the same way. Our own address is
// derived from our peer ID once and stored in the config. A peer announces its
// own address, and our view of it is the announced one if it is free in our
// subnet, otherwise one derived from the peer ID. The views of different nodes
// do not have to match: on receive the tunnel sets src to our view of the
// sender and dst to our own address.

// maxAddrAttempts bounds the search for a free address in pickAddr. With the
// prefix lengths we accept it is never reached; it only keeps the loop finite.
const maxAddrAttempts = 1 << 16

// ErrIPv6Disabled is returned by AllocPeerIPv6Unlocked when the IPv6 overlay is
// off on our side.
var ErrIPv6Disabled = errors.New("IPv6 overlay is disabled")

// CheckPeerIPUnlocked reports whether ip, set by the user, may be our view of
// the IPv4 address of peer exceptPeerID. Caller must hold the config lock;
// to store the address, the write lock in the same critical section.
func (c *Config) CheckPeerIPUnlocked(ip, exceptPeerID string) error {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return fmt.Errorf("invalid IP %s: %w", ip, err)
	}
	prefix := c.vpnPrefixUnlocked()
	if !prefix.IsValid() {
		return fmt.Errorf("invalid vpn.ipNet %q", c.VPNConfig.IPNet)
	}

	return c.checkPeerAddrUnlocked(addr, prefix, exceptPeerID)
}

// AllocPeerIPUnlocked picks our view of the IPv4 address of peerID, which
// announced the address announced (empty if none): see pickAddr. The announced
// legacyOwnIP counts as none. Addresses in except are skipped as if taken.
// rejectErr says why the announced address was not taken. To store the result,
// caller must hold the config write lock and store it in the same critical
// section, so that two peers cannot be given the same address.
func (c *Config) AllocPeerIPUnlocked(peerID, announced string, except []netip.Addr) (addr netip.Addr, rejectErr, err error) {
	prefix := c.vpnPrefixUnlocked()
	if !prefix.IsValid() {
		return netip.Addr{}, nil, fmt.Errorf("invalid vpn.ipNet %q", c.VPNConfig.IPNet)
	}
	if announced == legacyOwnIP {
		announced = ""
	}

	return pickAddr(peerID, prefix, announced, func(a netip.Addr) error {
		if slices.Contains(except, a) {
			return fmt.Errorf("IP %s is suggested for another peer", a)
		}
		return c.checkPeerAddrUnlocked(a, prefix, peerID)
	})
}

// AllocPeerIPv6Unlocked picks our view of the IPv6 address of peerID, which
// announced the address announced: see pickAddr. rejectErr says why the
// announced address was not taken. Caller must hold the config write lock and
// store the result in the same critical section, so that two peers cannot be
// given the same address.
func (c *Config) AllocPeerIPv6Unlocked(peerID, announced string) (addr netip.Addr, rejectErr, err error) {
	prefix, ok := c.vpnPrefixV6Unlocked()
	if !ok {
		return netip.Addr{}, nil, ErrIPv6Disabled
	}

	return pickAddr(peerID, prefix, announced, func(a netip.Addr) error {
		return c.checkPeerAddrUnlocked(a, prefix, peerID)
	})
}

// PeerIPv6Unlocked returns the IPv6 address of kp for routing and DNS. ok is
// false while either side has IPv6 off. The stored address is not re-validated:
// it was checked when assigned (AllocPeerIPv6Unlocked), and editing it by hand
// is on whoever does it.
func (c *Config) PeerIPv6Unlocked(kp KnownPeer) (netip.Addr, bool) {
	if !kp.RemoteIPv6Enabled || c.VPNConfig.IPNetV6 == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(kp.IPAddrV6)
	if err != nil {
		return netip.Addr{}, false
	}

	return addr, true
}

// checkPeerAddrUnlocked reports whether addr may be our view of the address of
// peer exceptPeerID inside our subnet prefix, IPv4 or IPv6: a host address of
// prefix (see checkHostAddr) that is not our own, not the DNS IP and not used
// by another known peer.
func (c *Config) checkPeerAddrUnlocked(addr netip.Addr, prefix netip.Prefix, exceptPeerID string) error {
	if err := checkHostAddr(addr, prefix); err != nil {
		return err
	}
	if addr == prefix.Addr() {
		return fmt.Errorf("IP %s is the local node's own address", addr)
	}
	if addr == c.netstackDNSIP {
		return fmt.Errorf("IP %s is reserved for the awl DNS server", addr)
	}
	for _, known := range c.KnownPeers {
		if known.PeerID == exceptPeerID {
			continue
		}
		peerIP := known.IPAddr
		if prefix.Addr().Is6() {
			peerIP = known.IPAddrV6
		}
		if used, err := netip.ParseAddr(peerIP); err == nil && used == addr {
			return fmt.Errorf("IP %s is already used by peer %s", addr, known.Alias)
		}
	}

	return nil
}

// checkHostAddr reports whether addr can be a host address in prefix: an
// address of the same family inside prefix that is not the network address
// (the Subnet-Router anycast address for IPv6) and not the IPv4 broadcast
// address.
func checkHostAddr(addr netip.Addr, prefix netip.Prefix) error {
	if prefix.Addr().Is4() {
		if !addr.Is4() {
			return fmt.Errorf("IP %s is not an IPv4 address", addr)
		}
	} else if !addr.Is6() || addr.Is4In6() {
		return fmt.Errorf("IP %s is not an IPv6 address", addr)
	}
	if !prefix.Contains(addr) {
		return fmt.Errorf("IP %s does not belong to subnet %s", addr, prefix.Masked())
	}
	if addr == prefix.Masked().Addr() {
		return fmt.Errorf("IP %s is the network address of subnet %s", addr, prefix.Masked())
	}
	if prefix.Addr().Is4() && addr == broadcastAddr(prefix) {
		return fmt.Errorf("IP %s is the broadcast address of subnet %s", addr, prefix.Masked())
	}

	return nil
}

// pickAddr picks an address inside prefix that check accepts: the announced
// one, otherwise the first one counting up from deriveAddr(peerID). rejectErr
// says why the announced address was not taken; it is nil when nothing was
// announced. err is set without a valid peer ID or when no address is free.
func pickAddr(peerID string, prefix netip.Prefix, announced string, check func(netip.Addr) error) (addr netip.Addr, rejectErr, err error) {
	id, err := peer.Decode(peerID)
	if err != nil {
		return netip.Addr{}, nil, fmt.Errorf("decode peer id: %w", err)
	}

	if announced != "" {
		announcedAddr, err := netip.ParseAddr(announced)
		if err == nil {
			err = check(announcedAddr)
		}
		if err == nil {
			return announcedAddr, nil, nil
		}
		rejectErr = err
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

// deriveAddr deterministically maps a peer ID into prefix: the network bits
// come from prefix, the host bits from SHA-256([]byte(id)).
//
// Hash bits are taken at the same positions they occupy in the address (byte i
// of the address from byte i of the hash), not from the start of the hash. So
// the low bits of the result do not depend on the prefix length: for any IPv6
// prefix up to /64 the interface identifier is the same.
//
// The result is only a starting point: it may collide with another peer or be
// a reserved address (e.g. the all-zero host), see pickAddr. Works for both
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

// nextAddr returns the address following a, wrapping around to the first
// address of prefix.
func nextAddr(a netip.Addr, prefix netip.Prefix) netip.Addr {
	next := a.Next()
	if !next.IsValid() || !prefix.Contains(next) {
		return prefix.Masked().Addr()
	}
	return next
}

// broadcastAddr returns the last address of an IPv4 prefix.
func broadcastAddr(prefix netip.Prefix) netip.Addr {
	network := prefix.Masked().Addr().As4()
	hostMask := uint32(1)<<(32-prefix.Bits()) - 1
	binary.BigEndian.PutUint32(network[:], binary.BigEndian.Uint32(network[:])|hostMask)
	return netip.AddrFrom4(network)
}
