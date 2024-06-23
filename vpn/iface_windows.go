//go:build windows
// +build windows

package vpn

import (
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/elevate"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

var WintunGUID *windows.GUID

func init() {
	var err error
	tun.WintunTunnelType = "Anywherelan"
	guid, err := windows.GUIDFromString("{13b1820f-bcf0-4eef-ba5d-9e98f7283a26}")
	if err != nil {
		panic(err)
	}
	WintunGUID = &guid
	tun.WintunStaticRequestedGUID = &guid
}

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask) (tun.Device, error) {
	var tunDevice tun.Device
	err := elevate.DoAsSystem(func() error {
		var err error
		tunDevice, err = tun.CreateTUNWithRequestedGUID(ifname, WintunGUID, mtu)
		if err != nil {
			return fmt.Errorf("create tun: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("do as system: %v", err)
	}

	nativeTunDevice := tunDevice.(*tun.NativeTun)
	luid := winipcfg.LUID(nativeTunDevice.LUID())

	ones, _ := ipMask.Size()
	netipAddr := netip.MustParseAddr(localIP.String())
	prefix := netip.PrefixFrom(netipAddr, ones)

	err = luid.SetIPAddresses([]netip.Prefix{prefix})
	if err != nil {
		return nil, fmt.Errorf("unable to setup interface IP: %v", err)
	}

	return tunDevice, nil
}

func (d *Device) InterfaceName() (string, error) {
	nativeTun := d.tun.(*tun.NativeTun)
	luid := winipcfg.LUID(nativeTun.LUID())
	guid, err := luid.GUID()
	if err != nil {
		return "", err
	}

	return guid.String(), nil
}
