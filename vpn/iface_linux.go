//go:build linux && !android
// +build linux,!android

package vpn

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask, localIPv6 net.IP, ipMaskv6 net.IPMask) (tun.Device, error) {
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

	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   localIP,
			Mask: ipMask,
		},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return nil, fmt.Errorf("unable to set IP (%s) to (%v on interface): %v", localIP, addr.IPNet, err)
	}

	if localIPv6 != nil {
		addr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   localIPv6,
				Mask: ipMaskv6,
			},
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return nil, fmt.Errorf("unable to set IPv6 (%s) to (%v on interface): %v", localIPv6, addr.IPNet, err)
		}
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("unable to UP interface: %v", err)
	}

	success = true
	return tunDevice, nil
}

func (d *Device) InterfaceName() (string, error) {
	interfaceName, err := d.tun.Name()
	if err != nil {
		return "", err
	}

	return interfaceName, nil
}
