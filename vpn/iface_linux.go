//go:build linux && !android
// +build linux,!android

package vpn

import (
	"fmt"
	"net"

	"github.com/milosgajdos/tenus"
	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask) (tun.Device, error) {
	ipNet := &net.IPNet{
		IP:   localIP.Mask(ipMask),
		Mask: ipMask,
	}

	tunDevice, err := tun.CreateTUN(ifname, mtu)
	if err != nil {
		return nil, fmt.Errorf("create tun: %v", err)
	}

	link, err := tenus.NewLinkFrom(ifname)
	if nil != err {
		return nil, fmt.Errorf("unable to get interface info: %v", err)
	}

	err = link.SetLinkIp(localIP, ipNet)
	if err != nil {
		return nil, fmt.Errorf("unable to set IP (%s) to (%v on interface): %v", localIP, ipNet, err)
	}

	err = link.SetLinkUp()
	if err != nil {
		return nil, fmt.Errorf("unable to UP interface: %v", err)
	}

	return tunDevice, nil
}

func (d *Device) InterfaceName() (string, error) {
	interfaceName, err := d.tun.Name()
	if err != nil {
		return "", err
	}

	return interfaceName, nil
}
