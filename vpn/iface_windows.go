// +build windows

package vpn

import (
	"fmt"
	"net"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/wintun"
	"golang.zx2c4.com/wireguard/windows/elevate"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func init() {
	tun.WintunPool, _ = wintun.MakePool("Anywherelan")
	// TODO: generate own GUID
	tun.WintunStaticRequestedGUID = &windows.GUID{12, 12, 12, [8]byte{12, 12, 12, 12, 12, 12, 12, 12}}
}

func newTUN(ifname string, mtu int, localIP net.IP, ipMask net.IPMask) (tun.Device, error) {
	var tunDevice tun.Device
	err := elevate.DoAsSystem(func() error {
		var err error
		tunDevice, err = tun.CreateTUN(ifname, mtu)
		return err
	})
	if err != nil {
		return nil, err
	}

	nativeTunDevice := tunDevice.(*tun.NativeTun)
	luid := winipcfg.LUID(nativeTunDevice.LUID())
	err = luid.SetIPAddresses([]net.IPNet{{localIP, ipMask}})
	if err != nil {
		return nil, fmt.Errorf("unable to setup interface: %v", err)
	}

	return tunDevice, nil
}
