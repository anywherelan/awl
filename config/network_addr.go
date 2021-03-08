package config

import (
	"encoding/binary"
	"net"
)

const (
	networkSubnet = "127.16.0.1/21"
)

var (
	ipNet *net.IPNet
)

func init() {
	var err error
	_, ipNet, err = net.ParseCIDR(networkSubnet)
	if err != nil {
		panic(err)
	}
}

// GenerateNextIpAddr is not thread safe.
func (c *Config) GenerateNextIpAddr() string {
	maxIp := ipNet.IP
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
