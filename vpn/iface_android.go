//go:build linux && android

package vpn

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

// NewAndroidTUNFromFD wraps a file descriptor handed in by the host app's
// VpnService (from establish().detachFd()) into a tun.Device. The returned
// device takes ownership of fd and closes it on Close.
//
// On Android the awl process never creates the TUN itself — routing is owned by
// VpnService.Builder, and changing routes (e.g. toggling VPN gateway mode)
// requires a fresh establish() and therefore a new fd. The host passes that fd
// down to gomobile, which wraps it here and swaps it into the running
// SwappableTUN without restarting the rest of awl.
func NewAndroidTUNFromFD(fd int) (tun.Device, error) {
	tunDevice, _, err := tun.CreateUnmonitoredTUNFromFD(fd)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("CreateUnmonitoredTUNFromFD: %v", err)
	}

	return tunDevice, nil
}

// newTUN is the nil-device path of NewDevice. On Android the TUN device must be
// supplied externally via NewAndroidTUNFromFD (the host owns the fd), so being
// asked to create one here is a programming error.
func newTUN(_ string, _ int, _ net.IP, _ net.IPMask) (tun.Device, error) {
	return nil, fmt.Errorf("android requires an externally-supplied tun device (use NewAndroidTUNFromFD)")
}

func (d *Device) InterfaceName() (string, error) {
	interfaceName, err := d.tun.Name()
	if err != nil {
		return "", err
	}

	return interfaceName, nil
}
