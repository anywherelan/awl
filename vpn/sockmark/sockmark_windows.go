//go:build windows

// TODO(gateway-windows): VPN gateway mode is not yet wired up on Windows.
// Outstanding work before it can be enabled in application.go:setupGateway:
//
//  1. Construct the marker via NewWindows(tunIfName) once the TUN is up so
//     that ControlFunc returns a non-nil function. application.go then has
//     to switch from the default sockmark.New() to NewWindows. Without
//     this, libp2p binds to the TUN once the /1 routes are installed,
//     creating a routing loop.
//
//  2. Add DNS-leak protection for gateway mode. Without it, the OS resolver
//     still queries the LAN DNS and DNS leaks past the TUN. This should go
//     through the shared ts-dns path in application.go (DNSService) rather
//     than a Windows-specific implementation.
//
//  3. nat_windows.go currently only enables IP forwarding (netsh) and does
//     not implement MASQUERADE. The exit node only works when the host has
//     a public/routable IP. A proper implementation needs RRAS, ICS, or a
//     WFP callout driver — see the long comment in nat_windows.go.
//
//  4. Re-detect the physical interface on network change events
//     (Wi-Fi <-> Ethernet); otherwise libp2p stays bound to a stale
//     interface after roaming.
//
// Until that is done, service/gateway.go Supported() returns an error early on
// runtime.GOOS == "windows" so the half-finished code below is unreachable.

package sockmark

import (
	"fmt"
	"math/bits"
	"net"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	// IP_UNICAST_IF / IPV6_UNICAST_IF socket-option ids.
	ipUnicastIF   = 31
	ipv6UnicastIF = 31
)

// WindowsMarker binds libp2p sockets to a specific NIC via IP_UNICAST_IF.
// physicalIfIndex is atomic to support a future re-detection on roaming
// (TODO at the top of this file).
type WindowsMarker struct {
	physicalIfIndex atomic.Uint32
}

// New returns a WindowsMarker without a configured physical interface.
// ControlFunc returns nil until SetPhysicalInterface succeeds.
func New() Marker { return &WindowsMarker{} }

// NewWindows constructs a marker and auto-detects the physical NIC against
// tunIfName. Returns an error if no suitable interface is found.
func NewWindows(tunIfName string) (*WindowsMarker, error) {
	m := &WindowsMarker{}
	if err := m.SetPhysicalInterface(tunIfName); err != nil {
		return nil, err
	}
	return m, nil
}

// SetPhysicalInterface picks the lowest-index interface that is not the TUN,
// is up, is not loopback, and has at least one IP. A more reliable
// implementation would inspect the routing table — see the TODO at the top
// of this file.
func (m *WindowsMarker) SetPhysicalInterface(tunIfName string) error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("list interfaces: %w", err)
	}

	var bestIdx uint32
	found := false
	for _, iface := range ifaces {
		if iface.Name == tunIfName {
			continue
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		idx := uint32(iface.Index)
		if !found || idx < bestIdx {
			bestIdx = idx
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no suitable physical interface found")
	}
	m.physicalIfIndex.Store(bestIdx)
	return nil
}

func (m *WindowsMarker) FWMark() uint32 { return 0 }

func (m *WindowsMarker) ControlFunc() func(network, address string, c syscall.RawConn) error {
	if m.physicalIfIndex.Load() == 0 {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		idx := m.physicalIfIndex.Load()
		if idx == 0 {
			return nil
		}
		var sockErr error
		err := c.Control(func(fd uintptr) {
			handle := windows.Handle(fd)
			// IP_UNICAST_IF (IPv4) takes the index in network byte order
			// (htonl). bits.ReverseBytes32 is the portable equivalent.
			sockErr = windows.SetsockoptInt(handle, windows.IPPROTO_IP, ipUnicastIF, int(bits.ReverseBytes32(idx)))
			if sockErr != nil {
				return
			}
			// IPV6_UNICAST_IF takes host byte order.
			sockErr = windows.SetsockoptInt(handle, windows.IPPROTO_IPV6, ipv6UnicastIF, int(idx))
		})
		if err != nil {
			return fmt.Errorf("sockmark control: %w", err)
		}
		if sockErr != nil {
			return fmt.Errorf("sockmark IP_UNICAST_IF: %w", sockErr)
		}
		return nil
	}
}
