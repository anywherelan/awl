package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
)

const (
	DefaultVPNInterfaceName = "awl0"
	// DefaultVPNNetworkSubnet is the awl subnet. The zero host part means
	// "derive our address from the peer ID", see deriveOwnAddrsUnlocked.
	// TODO: generate subnets if this has already taken
	DefaultVPNNetworkSubnet  = "10.64.0.0/11"
	DefaultVPNNetworkSubnet6 = "fd00:66:0::/48"
)

const (
	// maxIPv4PrefixBits rejects /31 and /32: they leave no address besides our own.
	maxIPv4PrefixBits = 30
	// maxIPv6PrefixBits keeps at least 64 host bits, so that addresses derived
	// from peer IDs practically never collide.
	maxIPv6PrefixBits = 64
)

// legacyOwnIP is our own address in every config from before v0.21.0, older
// versions did not derive it. A peer that announces it tells us nothing, see
// AllocPeerIPUnlocked.
const legacyOwnIP = "10.66.0.1"

// legacyIPNets are the vpn.ipNet defaults of older versions: /24 before
// v0.18.0, /16 since. See migrateLegacyIPNetUnlocked.
var legacyIPNets = []string{legacyOwnIP + "/24", legacyOwnIP + "/16"}

// migratedIPNet replaces legacyIPNets: the same own address in the awl subnet.
// Not DefaultVPNNetworkSubnet: its zero host part would give us a new address.
const migratedIPNet = legacyOwnIP + "/11"

// VPNPrefix returns our IPv4 address with the prefix length of the awl subnet.
// An invalid vpn.ipNet aborts startup, see ValidateForStartup, so the result
// is valid once Application.Init has started; before that it may be the zero
// Prefix.
func (c *Config) VPNPrefix() netip.Prefix {
	c.RLock()
	defer c.RUnlock()

	return c.vpnPrefixUnlocked()
}

// vpnPrefixUnlocked parses vpn.ipNet; the zero Prefix means it is invalid.
func (c *Config) vpnPrefixUnlocked() netip.Prefix {
	prefix, _ := parseIPNet(c.VPNConfig.IPNet)
	return prefix
}

// parseIPNet parses vpnConfig.ipNet: our own address with the prefix length of
// the awl subnet. A zero host part means "derive our address from the peer
// ID", see deriveOwnAddrsUnlocked. The returned prefix is not masked.
func parseIPNet(s string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, errors.New("not an IPv4 prefix")
	}
	if prefix.Bits() > maxIPv4PrefixBits {
		return netip.Prefix{}, fmt.Errorf("prefix /%d is too long, must be /%d or shorter", prefix.Bits(), maxIPv4PrefixBits)
	}

	return prefix, nil
}

// VPNPrefixV6 returns our IPv6 address with the prefix length of the awl IPv6
// subnet. When IPv6 is off, ok is false and the prefix is invalid.
func (c *Config) VPNPrefixV6() (prefix netip.Prefix, ok bool) {
	c.RLock()
	defer c.RUnlock()

	return c.vpnPrefixV6Unlocked()
}

// vpnPrefixV6Unlocked returns our IPv6 address with the prefix length of the
// awl IPv6 subnet. ok is false when IPv6 is disabled (vpn.ipNetV6 is empty) or
// the value is invalid; the latter aborts startup, see ValidateForStartup.
func (c *Config) vpnPrefixV6Unlocked() (prefix netip.Prefix, ok bool) {
	prefix, err := parseIPNetV6(c.VPNConfig.IPNetV6)
	return prefix, err == nil
}

// parseIPNetV6 parses vpnConfig.ipNetV6: our own address with the prefix length
// of the awl IPv6 subnet. A zero host part means "derive our address from the
// peer ID", see deriveOwnAddrsUnlocked. The returned prefix is not masked.
func parseIPNetV6(s string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
		return netip.Prefix{}, errors.New("not an IPv6 prefix")
	}
	if prefix.Bits() > maxIPv6PrefixBits {
		return netip.Prefix{}, fmt.Errorf("prefix /%d is too long, must be /%d or shorter", prefix.Bits(), maxIPv6PrefixBits)
	}

	return prefix, nil
}

// migrateLegacyIPNetUnlocked replaces vpn.ipNet that is one of legacyIPNets
// with migratedIPNet. Our own address stays, and so do the addresses of known
// peers: they lie inside the wider subnet. A value set by hand is kept.
func (c *Config) migrateLegacyIPNetUnlocked() {
	if !slices.Contains(legacyIPNets, c.VPNConfig.IPNet) {
		return
	}

	logger.Infof("widened the awl network: vpn.ipNet %s changed to %s, the addresses stay the same", c.VPNConfig.IPNet, migratedIPNet)
	c.VPNConfig.IPNet = migratedIPNet
}

// deriveOwnAddrsUnlocked replaces a zero host part of vpn.ipNet and
// vpn.ipNetV6 (as in DefaultVPNNetworkSubnet and DefaultVPNNetworkSubnet6,
// meaning "not chosen yet") with an address derived from our peer ID. The
// result is stored, so it does not change afterwards; an address set by hand
// is kept. Known peers are not checked: on the first start there are none,
// and our own address matters more. An invalid value is left for
// ValidateForStartup to report.
func (c *Config) deriveOwnAddrsUnlocked() {
	unchosen := func(prefix netip.Prefix) bool {
		return prefix.IsValid() && prefix.Addr() == prefix.Masked().Addr()
	}

	if prefix := c.vpnPrefixUnlocked(); unchosen(prefix) {
		if derived, ok := derivedOwnPrefix(c.P2pNode.PeerID, prefix); ok {
			c.VPNConfig.IPNet = derived.String()
		}
	}
	if prefix, _ := c.vpnPrefixV6Unlocked(); unchosen(prefix) {
		if derived, ok := derivedOwnPrefix(c.P2pNode.PeerID, prefix); ok {
			c.VPNConfig.IPNetV6 = derived.String()
		}
	}
}

// derivedOwnPrefix returns prefix with our own address derived from peerID in
// place of its host part. Only the network and broadcast addresses are
// skipped. ok is false without a valid peer ID.
func derivedOwnPrefix(peerID string, prefix netip.Prefix) (netip.Prefix, bool) {
	addr, _, err := pickAddr(peerID, prefix, "", func(a netip.Addr) error {
		return checkHostAddr(a, prefix)
	})
	return netip.PrefixFrom(addr, prefix.Bits()), err == nil
}

// DerivedVPNAddr returns the IPv4 address derived from our peer ID in the awl
// subnet: our address if vpn.ipNet had a zero host part. ok is false without
// an identity or with an invalid vpn.ipNet.
func (c *Config) DerivedVPNAddr() (netip.Addr, bool) {
	c.RLock()
	defer c.RUnlock()

	prefix := c.vpnPrefixUnlocked()
	if !prefix.IsValid() {
		return netip.Addr{}, false
	}
	derived, ok := derivedOwnPrefix(c.P2pNode.PeerID, prefix)
	return derived.Addr(), ok
}

// NetstackDNSIP returns the in-subnet IP reserved for the awl DNS server,
// computed once in setDefaults and fixed for the session (see the
// netstackDNSIP field). nil when the subnet has no free address, in which case
// DNS is disabled. Used on Android, where DNS queries to it are intercepted
// inside the tunnel.
func (c *Config) NetstackDNSIP() net.IP {
	c.RLock()
	defer c.RUnlock()

	if !c.netstackDNSIP.IsValid() {
		return nil
	}
	return net.IP(c.netstackDNSIP.AsSlice())
}

// computeNetstackDNSIP derives the reserved DNS server IP from the current
// config snapshot: broadcast-1, shifted down until an address is free to give
// to a peer. Deterministic; the zero Addr when the subnet has no free address.
// Called from setDefaults, where netstackDNSIP is not set yet, so the check
// does not reject the address being chosen. The result is cached in
// netstackDNSIP and read everywhere else via NetstackDNSIP.
func (c *Config) computeNetstackDNSIP() netip.Addr {
	prefix := c.vpnPrefixUnlocked()
	if !prefix.IsValid() {
		return netip.Addr{}
	}

	network := prefix.Masked().Addr()
	for candidate := broadcastAddr(prefix).Prev(); candidate != network; candidate = candidate.Prev() {
		if c.checkPeerAddrUnlocked(candidate, prefix, "") == nil {
			return candidate
		}
	}

	return netip.Addr{}
}
