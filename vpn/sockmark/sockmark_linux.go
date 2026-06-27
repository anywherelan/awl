//go:build linux && !android

package sockmark

import (
	"fmt"
	"syscall"
)

// fwMark is the firewall mark applied to libp2p sockets to bypass the VPN
// gateway. Used together with policy routing (ip rule) so that libp2p traffic
// uses the physical interface instead of the TUN. 0x61776C = "awl" in ASCII;
// the routes package uses the same numeric value as its routing table ID, so
// awl-owned entries are easy to spot in `ip rule` / `ip route show table`.
const fwMark uint32 = 0x61776C

type linuxMarker struct{}

// New constructs a Marker for Linux. Setting SO_MARK requires CAP_NET_ADMIN,
// which AWL already needs for TUN setup, so no extra capability is required.
func New() Marker {
	return linuxMarker{}
}

func (linuxMarker) FWMark() uint32 { return fwMark }

func (linuxMarker) ControlFunc() func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, int(fwMark))
		})
		if err != nil {
			return fmt.Errorf("sockmark control: %w", err)
		}
		if sockErr != nil {
			return fmt.Errorf("sockmark SO_MARK: %w", sockErr)
		}
		return nil
	}
}
