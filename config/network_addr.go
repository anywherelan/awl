package config

import (
	"encoding/binary"
	"net"
)

const (
	defaultInterfaceName = "awl0"
	// TODO: generate subnets if this has already taken
	defaultNetworkSubnet = "10.66.0.1/24"
)

// GenerateNextIpAddr is not thread safe.
func (c *Config) GenerateNextIpAddr() string {
	localIP, netMask := c.VPNLocalIPMask()
	ipNet := net.IPNet{
		IP:   localIP.Mask(netMask),
		Mask: netMask,
	}
	maxIp := localIP
	for _, known := range c.KnownPeers {
		ip := net.ParseIP(known.IPAddr)
		if ip == nil {
			continue
		}
		ip = ip.To4()

		if ipNet.Contains(ip) && binary.BigEndian.Uint32(ip) > binary.BigEndian.Uint32(maxIp) {
			maxIp = ip
		}
	}

	newIp := incrementIPAddr(maxIp)

	return newIp.String()
}

func incrementIPAddr(ip net.IP) net.IP {
	i := binary.BigEndian.Uint32(ip)
	i++

	bs := make([]byte, 4)
	binary.BigEndian.PutUint32(bs, i)

	ipNew := net.IP(bs)
	return ipNew
}
