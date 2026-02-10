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
	DefaultVPNNetworkSubnet = "10.66.0.1/24"
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

	// Find next available IP that is not in exceptMap
	for {
		newIp := incrementIPAddr(maxIp)
		newIpStr := newIp.String()

		if _, excluded := exceptMap[newIpStr]; !excluded {
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
	i := binary.BigEndian.Uint32(ip)
	i++

	bs := make([]byte, 4)
	binary.BigEndian.PutUint32(bs, i)

	ipNew := net.IP(bs)
	return ipNew
}
