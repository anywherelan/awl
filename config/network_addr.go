package config

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	DefaultVPNInterfaceName = "awl0"
	// TODO: generate subnets if this has already taken
	DefaultVPNNetworkSubnet  = "10.66.0.1/16"
	DefaultVPNNetworkSubnet6 = "fd00:66:0::/48"
)

// VPNPrefix returns our IPv4 address with the prefix length of the awl subnet.
// setDefaults replaces an invalid vpn.ipNet with the default, so the result is
// valid for any config built by NewConfig or LoadConfig.
func (c *Config) VPNPrefix() netip.Prefix {
	c.RLock()
	defer c.RUnlock()

	return c.vpnPrefixUnlocked()
}

// TODO: remove after unification with ipv6
func (c *Config) VPNLocalIPMaskUnlocked() (net.IP, net.IPMask) {
	prefix := c.vpnPrefixUnlocked()
	if !prefix.IsValid() {
		logger.Errorf("invalid vpn.ipNet %q", c.VPNConfig.IPNet)
		return nil, nil
	}

	return net.IP(prefix.Addr().AsSlice()), net.CIDRMask(prefix.Bits(), net.IPv4len*8)
}

// vpnPrefixUnlocked parses vpn.ipNet; the zero Prefix means it is invalid.
func (c *Config) vpnPrefixUnlocked() netip.Prefix {
	prefix, err := netip.ParsePrefix(c.VPNConfig.IPNet)
	if err != nil || !prefix.Addr().Is4() {
		return netip.Prefix{}
	}

	return prefix
}

// VPNPrefixV6 returns our IPv6 address with the prefix length of the awl IPv6
// subnet. When IPv6 is off, ok is false and the prefix is invalid.
func (c *Config) VPNPrefixV6() (prefix netip.Prefix, ok bool) {
	c.RLock()
	defer c.RUnlock()

	return c.vpnPrefixV6Unlocked()
}

// ErrIPv6Disabled is returned by AllocPeerIPv6Unlocked when the IPv6 overlay is
// off on our side.
var ErrIPv6Disabled = errors.New("IPv6 overlay is disabled")

// maxIPv6PrefixBits keeps at least 64 host bits, so that addresses derived from
// peer IDs practically never collide.
const maxIPv6PrefixBits = 64

// parseIPNetV6 parses vpnConfig.ipNetV6: our own address with the prefix length
// of the awl IPv6 subnet. A zero host part means "derive our address from the
// peer ID", see deriveOwnIPv6Unlocked. The returned prefix is not masked.
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

// vpnPrefixV6Unlocked returns our IPv6 address with the prefix length of the
// awl IPv6 subnet. ok is false when IPv6 is disabled or the value is invalid;
// the latter aborts startup, see ValidateForStartup.
func (c *Config) vpnPrefixV6Unlocked() (prefix netip.Prefix, ok bool) {
	if c.VPNConfig.IPNetV6 == "" {
		return netip.Prefix{}, false
	}
	prefix, err := parseIPNetV6(c.VPNConfig.IPNetV6)
	return prefix, err == nil
}

// AllocPeerIPv6Unlocked picks our view of the IPv6 address of peerID, which
// announced the address announced: see pickPeerAddr. rejectErr says why the
// announced address was not taken. Caller must hold the config write lock and
// store the result in the same critical section, so that two peers cannot be
// given the same address.
func (c *Config) AllocPeerIPv6Unlocked(peerID, announced string) (addr string, rejectErr, err error) {
	prefix, ok := c.vpnPrefixV6Unlocked()
	if !ok {
		return "", nil, ErrIPv6Disabled
	}
	id, err := peer.Decode(peerID)
	if err != nil {
		return "", nil, fmt.Errorf("decode peer id: %w", err)
	}

	picked, rejectErr, err := pickPeerAddr(id, prefix, announced, func(a netip.Addr) error {
		return c.checkPeerIPv6Unlocked(a, peerID)
	})
	if err != nil {
		return "", rejectErr, err
	}

	return picked.String(), rejectErr, nil
}

// checkPeerIPv6Unlocked reports whether addr may be our view of the IPv6
// address of peer exceptPeerID: an IPv6 address inside our subnet that is not
// reserved, not our own and not used by another known peer.
func (c *Config) checkPeerIPv6Unlocked(addr netip.Addr, exceptPeerID string) error {
	prefix, ok := c.vpnPrefixV6Unlocked()
	if !ok {
		return ErrIPv6Disabled
	}

	if !addr.Is6() || addr.Is4In6() {
		return fmt.Errorf("%s is not an IPv6 address", addr)
	}
	if !prefix.Contains(addr) {
		return fmt.Errorf("%s does not belong to subnet %s", addr, prefix.Masked())
	}
	if addr == prefix.Masked().Addr() {
		return fmt.Errorf("%s is the Subnet-Router anycast address of %s", addr, prefix.Masked())
	}
	if addr == prefix.Addr() {
		return fmt.Errorf("%s is the local node's own address", addr)
	}
	for _, known := range c.KnownPeers {
		if known.PeerID == exceptPeerID {
			continue
		}
		if used, err := netip.ParseAddr(known.IPAddrV6); err == nil && used == addr {
			return fmt.Errorf("%s is already used by peer %s", addr, known.Alias)
		}
	}

	return nil
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

// NetstackDNSIP returns the in-subnet IP reserved for the awl DNS server,
// computed once in setDefaults and fixed for the session (see the
// netstackDNSIP field). nil when the subnet has no free address, in which case
// DNS is disabled. Used on Android, where DNS queries to it are intercepted
// inside the tunnel.
func (c *Config) NetstackDNSIP() net.IP {
	c.RLock()
	defer c.RUnlock()

	return c.netstackDNSIP
}

// computeNetstackDNSIP derives the reserved DNS server IP from the current
// config snapshot: broadcast-1, shifted down until an address is free to
// assign (CheckIPUnique rejects our own IP, the broadcast address and peers).
// Deterministic; returns nil when the subnet has no free address. Not thread
// safe — called from setDefaults at construction only, where netstackDNSIP is
// still nil, so CheckIPUnique's own DNS-reservation check is a no-op here and
// cannot recurse. The result is cached in netstackDNSIP and read everywhere
// else via NetstackDNSIP.
func (c *Config) computeNetstackDNSIP() net.IP {
	localIP, netMask := c.VPNLocalIPMaskUnlocked()
	if localIP == nil {
		return nil
	}
	ipNet := &net.IPNet{
		IP:   localIP.Mask(netMask),
		Mask: netMask,
	}
	network, broadcast := subnetBounds(ipNet)

	for candidate := broadcast - 1; candidate > network; candidate-- {
		ip := uint32ToIPAddr(candidate)
		if c.CheckIPUnique(ip.String(), "") == nil {
			return ip
		}
	}

	return nil
}

// GenerateNextIpAddr is not thread safe.
func (c *Config) GenerateNextIpAddr() string {
	return c.GenerateNextIpAddrExcept(nil)
}

// GenerateNextIpAddrExcept is not thread safe.
func (c *Config) GenerateNextIpAddrExcept(except []string) string {
	localIP, netMask := c.VPNLocalIPMaskUnlocked()
	ipNet := &net.IPNet{
		IP:   localIP.Mask(netMask),
		Mask: netMask,
	}

	maxIp := localIP
	for _, known := range c.KnownPeers {
		ip := net.ParseIP(known.IPAddr)
		if ip == nil {
			continue
		}
		// TODO: support ipv6
		ip = ip.To4()

		if ipNet.Contains(ip) && binary.BigEndian.Uint32(ip) > binary.BigEndian.Uint32(maxIp) {
			maxIp = ip
		}
	}

	exceptMap := make(map[string]struct{}, len(except))
	for _, ip := range except {
		exceptMap[ip] = struct{}{}
	}

	// Reserved addresses that must never be handed out to a peer.
	_, broadcast := subnetBounds(ipNet)

	// Find next available IP that is not in exceptMap and not reserved
	for {
		newIp := incrementIPAddr(maxIp)
		newIpStr := newIp.String()

		_, excluded := exceptMap[newIpStr]
		reserved := binary.BigEndian.Uint32(newIp) == broadcast || newIp.Equal(c.netstackDNSIP)
		if !excluded && !reserved {
			return newIpStr
		}

		maxIp = newIp
	}
}

// CheckIPUnique is not thread safe.
// Checks IP for: valid ip, unique across peers, in vpn net mask
func (c *Config) CheckIPUnique(checkIP string, exceptPeerID string) error {
	localIP, netMask := c.VPNLocalIPMaskUnlocked()
	ipNet := &net.IPNet{
		IP:   localIP.Mask(netMask),
		Mask: netMask,
	}

	ipv6, err := netip.ParseAddr(checkIP)
	if err != nil {
		return fmt.Errorf("invalid IP %s: %w", checkIP, err)
	}
	// TODO: support ipv6
	ipv4 := ipv6.As4()
	ip := net.IP(ipv4[:])

	contains := ipNet.Contains(ip)
	if !contains {
		return fmt.Errorf("IP %s does not belong to subnet %s", checkIP, ipNet)
	}

	if _, broadcast := subnetBounds(ipNet); binary.BigEndian.Uint32(ip) == broadcast {
		return fmt.Errorf("IP %s is the broadcast address of subnet %s", checkIP, ipNet)
	}
	if ip.Equal(localIP) {
		return fmt.Errorf("IP %s is the local node's own address", checkIP)
	}
	if ip.Equal(c.netstackDNSIP) {
		return fmt.Errorf("IP %s is reserved for the awl DNS server", checkIP)
	}

	for _, peer := range c.KnownPeers {
		if peer.IPAddr != checkIP {
			continue
		}
		if exceptPeerID != "" && peer.PeerID == exceptPeerID {
			continue
		}

		return fmt.Errorf("ip %s is already used by peer %s", checkIP, peer.Alias)
	}

	return nil
}

func incrementIPAddr(ip net.IP) net.IP {
	return uint32ToIPAddr(binary.BigEndian.Uint32(ip) + 1)
}

func uint32ToIPAddr(i uint32) net.IP {
	bs := make([]byte, 4)
	binary.BigEndian.PutUint32(bs, i)

	return bs
}

// subnetBounds returns the IPv4 network and broadcast addresses of ipNet.
func subnetBounds(ipNet *net.IPNet) (network, broadcast uint32) {
	network = binary.BigEndian.Uint32(ipNet.IP.Mask(ipNet.Mask))
	broadcast = network | ^binary.BigEndian.Uint32(ipNet.Mask)
	return network, broadcast
}
