package config

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
)

const (
	DefaultVPNInterfaceName = "awl0"
	// TODO: generate subnets if this has already taken
	DefaultVPNNetworkSubnet  = "10.66.0.1/16"
	DefaultVPNNetworkSubnet6 = "fd00:66:0::/48"
)

func (c *Config) VPNLocalIPMask() (net.IP, net.IPMask) {
	c.RLock()
	defer c.RUnlock()

	return c.VPNLocalIPMaskUnlocked()
}

func (c *Config) VPNLocalIPMaskUnlocked() (net.IP, net.IPMask) {
	localIP, ipNet, err := net.ParseCIDR(c.VPNConfig.IPNet)
	if err != nil {
		logger.Errorf("parse CIDR %s: %v", c.VPNConfig.IPNet, err)
		return nil, nil
	}
	return localIP.To4(), ipNet.Mask
}

func (c *Config) VPNLocalIPMaskV6() (net.IP, net.IPMask) {
	c.RLock()
	defer c.RUnlock()

	return c.VPNLocalIPMaskV6Unlocked()
}

func (c *Config) VPNLocalIPMaskV6Unlocked() (net.IP, net.IPMask) {
	if c.VPNConfig.IPNetV6 == "" {
		return nil, nil
	}
	localIP, ipNet, err := net.ParseCIDR(c.VPNConfig.IPNetV6)
	if err != nil {
		logger.Errorf("parse CIDR %s: %v", c.VPNConfig.IPNetV6, err)
		return nil, nil
	}

	return localIP.To16(), ipNet.Mask
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
