//go:build darwin
// +build darwin

package vpn

import (
	"fmt"
	"net"
	"os/exec"

	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask) (tun.Device, error) {
	ipNet := &net.IPNet{
		IP:   localIP,
		Mask: ipMask,
	}

	tunDevice, err := tun.CreateTUN(ifname, mtu)
	if err != nil {
		return nil, fmt.Errorf("create tun: %v", err)
	}
	// Interface name must be utun[0-9]*
	realIfname, err := tunDevice.Name()
	if err != nil {
		return nil, fmt.Errorf("get interface name: %v", err)
	}

	err = exec.Command("ifconfig", realIfname, "inet", ipNet.String(), localIP.String()).Run()
	if err != nil {
		return nil, fmt.Errorf("unable to setup interface mask: %v", err)
	}

	ipNetMasked := &net.IPNet{
		IP:   localIP.Mask(ipMask),
		Mask: ipMask,
	}
	err = exec.Command("route", "-q", "-n", "add", "-inet", ipNetMasked.String(), "-iface", realIfname).Run()
	if err != nil {
		return nil, fmt.Errorf("unable to setup interface route: %v", err)
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
