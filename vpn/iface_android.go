//go:build linux && android
// +build linux,android

package vpn

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

// TODO: refactor and remove this hack
const TunFDEnvKey = "AWL_TUN_FD"

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask) (tun.Device, error) {
	fdStr := os.Getenv(TunFDEnvKey)
	tunFD, err := strconv.ParseInt(fdStr, 10, 32)
	if err != nil || tunFD == 0 {
		return nil, fmt.Errorf("invalid tun FD %s: %v", fdStr, err)
	}

	tunDevice, _, err := tun.CreateUnmonitoredTUNFromFD(int(tunFD))
	if err != nil {
		unix.Close(int(tunFD))
		return nil, fmt.Errorf("CreateUnmonitoredTUNFromFD: %v", err)
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
