//go:build freebsd

package vpn

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/ipfs/go-log/v2"
	"golang.zx2c4.com/wireguard/tun"
)

func newTUN(ifname string, mtu int, prefix, prefixV6 netip.Prefix) (tun.Device, error) {
	if err := destroyStaleTUN(ifname); err != nil {
		return nil, err
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

	// CreateTUN takes the interface out of point-to-point mode, so the kernel
	// adds the route to the awl subnet by itself.
	if out, err := exec.Command("ifconfig", ifname, "inet", prefix.String()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unable to set IP (%s) on interface: %v: %s", prefix, err, strings.TrimSpace(string(out)))
	}

	if prefixV6.IsValid() {
		if out, err := exec.Command("ifconfig", ifname, "inet6", prefixV6.Addr().String(),
			"prefixlen", strconv.Itoa(prefixV6.Bits())).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("unable to set IPv6 (%s) on interface: %v: %s", prefixV6, err, strings.TrimSpace(string(out)))
		}
	}

	if out, err := exec.Command("ifconfig", ifname, "up").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unable to UP interface: %v: %s", err, strings.TrimSpace(string(out)))
	}

	success = true
	return tunDevice, nil
}

// destroyStaleTUN destroys the TUN interface ifname if no process uses it.
// FreeBSD keeps a TUN interface after its process is killed, and CreateTUN
// fails while the name is taken. A TUN device can be opened by one process
// at a time, so the open fails with EBUSY if ifname belongs to a running awl.
func destroyStaleTUN(ifname string) error {
	if _, err := net.InterfaceByName(ifname); err != nil {
		return nil
	}

	file, err := os.OpenFile("/dev/"+ifname, os.O_RDWR, 0)
	switch {
	case errors.Is(err, syscall.EBUSY):
		return fmt.Errorf("interface %s is used by another process, is awl already running?", ifname)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("interface %s already exists and it is not a TUN interface", ifname)
	case err != nil:
		return fmt.Errorf("open existing interface %s: %v", ifname, err)
	}
	_ = file.Close()

	log.Logger("awl/vpn").Infof("interface %s already exists and is not used, destroying it", ifname)
	if out, err := exec.Command("ifconfig", ifname, "destroy").CombinedOutput(); err != nil {
		return fmt.Errorf("unable to destroy existing interface %s: %v: %s", ifname, err, strings.TrimSpace(string(out)))
	}

	return nil
}

func (d *Device) InterfaceName() (string, error) {
	interfaceName, err := d.tun.Name()
	if err != nil {
		return "", err
	}

	return interfaceName, nil
}
