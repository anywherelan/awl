//go:build darwin
// +build darwin

package vpn

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"

	"golang.zx2c4.com/wireguard/tun"
)

// TODO: untested
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

	err = exec.Command("ifconfig", realIfname, "inet", ipNet.String(), "mtu", strconv.FormatInt(int64(mtu), 10), "up").Run()
	if err != nil {
		return nil, fmt.Errorf("unable to setup interface: %v", err)
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
