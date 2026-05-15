//go:build windows
// +build windows

package vpn

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/ipfs/go-log/v2"
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
	logger := log.Logger("awl/vpn")

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

	// Wintun registers itself in NDIS with MTU=65535 (WINTUN_MAX_IP_PACKET_SIZE).
	// tun.CreateTUNWithRequestedGUID only stores mtu in wireguard-go's internal
	// forcedMTU field — it does NOT push it to the Windows IP stack. We have to
	// force NLMTU ourselves via winipcfg, otherwise local apps see MTU=65535,
	// send oversized packets, and the size check in ReadTUNPackets silently drops them.
	if err := setInterfaceMTU(logger, luid, winipcfg.AddressFamily(windows.AF_INET), uint32(mtu)); err != nil {
		tunDevice.Close()
		return nil, fmt.Errorf("set IPv4 MTU on tun: %v", err)
	}
	// TODO: support ipv6. Forwarding still ignores IPv6 packets (see Device.WritePacket),
	// but we set the system MTU best-effort so the interface is configured correctly once
	// IPv6 lands. On hosts with IPv6 disabled on the interface this Set() fails — that's
	// expected, not fatal.
	if err := setInterfaceMTU(logger, luid, winipcfg.AddressFamily(windows.AF_INET6), uint32(mtu)); err != nil {
		logger.Warnf("set IPv6 MTU on tun (best-effort, ipv6 unused by awl): %v", err)
	}

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

// setInterfaceMTU forces NLMTU on the given address family via winipcfg
// (SetIpInterfaceEntry under the hood — bypasses netsh's validation, which has
// regressed on some Windows 11 builds). Right after CreateAdapter the IP
// interface row may briefly return ERROR_NOT_FOUND while the stack settles, so
// we retry. After a successful Set() we read NLMTU back and warn if it doesn't
// match — that's the signature of a third-party LWF filter or a system MTU
// override silently changing our value.
func setInterfaceMTU(logger *log.ZapEventLogger, luid winipcfg.LUID, family winipcfg.AddressFamily, mtu uint32) error {
	const attempts = 10
	const delay = 100 * time.Millisecond

	var lastErr error
	for i := 0; i < attempts; i++ {
		iface, err := luid.IPInterface(family)
		if err != nil {
			lastErr = fmt.Errorf("get IPInterface: %w", err)
			time.Sleep(delay)
			continue
		}
		iface.NLMTU = mtu
		if err := iface.Set(); err != nil {
			lastErr = fmt.Errorf("set IPInterface: %w", err)
			time.Sleep(delay)
			continue
		}

		verify, err := luid.IPInterface(family)
		if err != nil {
			logger.Warnf("verify NLMTU after Set (family=%d): re-read failed: %v", family, err)
			return nil
		}
		if verify.NLMTU != mtu {
			logger.Warnf("system NLMTU=%d after setting %d (family=%d) — third-party LWF filter or Windows MTU regression likely overriding the value",
				verify.NLMTU, mtu, family)
		} else {
			logger.Infof("tun NLMTU set to %d (family=%d)", mtu, family)
		}
		return nil
	}
	return fmt.Errorf("after %d attempts: %w", attempts, lastErr)
}
