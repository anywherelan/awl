//go:build darwin
// +build darwin

package vpn

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask, localIPv6 net.IP, ipMaskv6 net.IPMask) (tun.Device, error) {
	ipNet := &net.IPNet{
		IP:   localIP,
		Mask: ipMask,
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
	// Interface name must be utun[0-9]*
	realIfname, err := tunDevice.Name()
	if err != nil {
		return nil, fmt.Errorf("get interface name: %v", err)
	}

	if out, err := exec.Command("ifconfig", realIfname, "inet", ipNet.String(), localIP.String()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unable to setup interface mask: %v: %s", err, strings.TrimSpace(string(out)))
	}

	ipNetMasked := &net.IPNet{
		IP:   localIP.Mask(ipMask),
		Mask: ipMask,
	}
	if out, err := exec.Command("route", "-q", "-n", "add", "-inet", ipNetMasked.String(), "-iface", realIfname).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unable to setup interface route: %v: %s", err, strings.TrimSpace(string(out)))
	}

	if localIPv6 != nil {
		ipNet6 := &net.IPNet{
			IP:   localIPv6,
			Mask: ipMaskv6,
		}
		prefixLen, _ := ipMaskv6.Size()
		if out, err := exec.Command("ifconfig", realIfname, "inet6", localIPv6.String(),
			"prefixlen", fmt.Sprintf("%d", prefixLen)).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("unable to set IPv6 (%s) on interface: %v: %s", ipNet6, err, strings.TrimSpace(string(out)))
		}
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
