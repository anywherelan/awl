//go:build !windows && !darwin && !linux
// +build !windows,!darwin,!linux

package vpn

import (
	"fmt"
	"net/netip"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/tuntest"
)

func newTUN(ifname string, mtu int, _, _ netip.Prefix) (tun.Device, error) {
	fmt.Println("WARN: TUN is unimplemented for !linux,!windows,!darwin")
	tt := tuntest.NewChannelTUN()

	go func() {
		for range tt.Inbound {
		}
	}()

	return tt.TUN(), nil
}

func (d *Device) InterfaceName() (string, error) {
	interfaceName, err := d.tun.Name()
	if err != nil {
		return "", err
	}

	return interfaceName, nil
}
