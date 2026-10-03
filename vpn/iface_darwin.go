//go:build darwin
// +build darwin

package vpn

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, prefix, prefixV6 netip.Prefix) (tun.Device, error) {
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

	if out, err := exec.Command("ifconfig", realIfname, "inet", prefix.String(), prefix.Addr().String()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unable to setup interface mask: %v: %s", err, strings.TrimSpace(string(out)))
	}

	if out, err := exec.Command("route", "-q", "-n", "add", "-inet", prefix.Masked().String(), "-iface", realIfname).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unable to setup interface route: %v: %s", err, strings.TrimSpace(string(out)))
	}

	if prefixV6.IsValid() {
		if out, err := exec.Command("ifconfig", realIfname, "inet6", prefixV6.Addr().String(),
			"prefixlen", fmt.Sprintf("%d", prefixV6.Bits())).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("unable to set IPv6 (%s) on interface: %v: %s", prefixV6, err, strings.TrimSpace(string(out)))
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
