//go:build linux && !android
// +build linux,!android

package vpn

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/ipfs/go-log/v2"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, prefix, prefixV6 netip.Prefix) (tun.Device, error) {
	// CreateTUN takes over an existing TUN interface with this name instead of
	// failing, so check for it first.
	if link, err := netlink.LinkByName(ifname); err == nil {
		log.Logger("awl/vpn").Infof("interface %s already exists, reusing it. Addresses on it: %s", ifname, linkAddrs(link))
	}

	tunDevice, err := tun.CreateTUN(ifname, mtu)
	if err != nil {
		return nil, fmt.Errorf("create tun: %v", err)
	}
	// Close the freshly created device if any later setup step fails, otherwise
	// the TUN interface leaks.
	success := false
	defer func() {
		if !success {
			_ = tunDevice.Close()
		}
	}()

	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("unable to get interface info: %v", err)
	}

	addr := &netlink.Addr{IPNet: prefixToIPNet(prefix)}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return nil, fmt.Errorf("unable to set IP (%s) to (%v on interface): %v", prefix.Addr(), addr.IPNet, err)
	}

	if prefixV6.IsValid() {
		addr := &netlink.Addr{IPNet: prefixToIPNet(prefixV6)}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return nil, fmt.Errorf("unable to set IPv6 (%s) to (%v on interface): %v", prefixV6.Addr(), addr.IPNet, err)
		}
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("unable to UP interface: %v", err)
	}

	success = true
	return tunDevice, nil
}

// linkAddrs lists the addresses on link for a log line.
func linkAddrs(link netlink.Link) string {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Sprintf("unknown (%v)", err)
	}
	if len(addrs) == 0 {
		return "none"
	}
	list := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		list = append(list, addr.IPNet.String())
	}
	return strings.Join(list, ", ")
}

func prefixToIPNet(prefix netip.Prefix) *net.IPNet {
	return &net.IPNet{
		IP:   prefix.Addr().AsSlice(),
		Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen()),
	}
}

func (d *Device) InterfaceName() (string, error) {
	interfaceName, err := d.tun.Name()
	if err != nil {
		return "", err
	}

	return interfaceName, nil
}
