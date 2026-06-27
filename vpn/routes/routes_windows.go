//go:build windows

package routes

import (
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// RouteState holds the state needed to teardown gateway routes on Windows.
type RouteState struct {
	tunLUID winipcfg.LUID
	routes  []winipcfg.MibIPforwardRow2
}

// SetupGatewayRoutes configures 0.0.0.0/1 + 128.0.0.0/1 routes via the TUN interface.
// These /1 routes are more specific than the existing /0 default gateway,
// so they win longest-prefix-match without replacing the original default route.
//
// fwmark is unused on Windows — sockets are bound to the physical interface
// via IP_UNICAST_IF; see vpn/sockmark/sockmark_windows.go.
func SetupGatewayRoutes(tunIfName string, fwmark uint32) (*RouteState, error) {
	// On Windows, tunIfName is the GUID string. Get the LUID from it.
	guid, err := windows.GUIDFromString(tunIfName)
	if err != nil {
		return nil, fmt.Errorf("parse TUN GUID %s: %w", tunIfName, err)
	}
	luid, err := winipcfg.LUIDFromGUID(&guid)
	if err != nil {
		return nil, fmt.Errorf("get LUID from GUID: %w", err)
	}

	state := &RouteState{
		tunLUID: luid,
	}

	// Add 0.0.0.0/1 and 128.0.0.0/1 via TUN with low metric
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/1"),
		netip.MustParsePrefix("128.0.0.0/1"),
	}

	for _, prefix := range prefixes {
		row := winipcfg.MibIPforwardRow2{}
		row.InterfaceLUID = luid
		row.DestinationPrefix.PrefixLength = uint8(prefix.Bits())
		if err := row.DestinationPrefix.RawPrefix.SetAddr(prefix.Addr()); err != nil {
			_ = TeardownGatewayRoutes(state)
			return nil, fmt.Errorf("set destination prefix %s: %w", prefix, err)
		}
		// NextHop: zero addr = on-link (point-to-point TUN)
		if err := row.NextHop.SetAddr(netip.IPv4Unspecified()); err != nil {
			_ = TeardownGatewayRoutes(state)
			return nil, fmt.Errorf("set next hop: %w", err)
		}
		row.Metric = 5 // low metric = high priority

		if err := row.Create(); err != nil {
			_ = TeardownGatewayRoutes(state)
			return nil, fmt.Errorf("add route %s: %w", prefix, err)
		}
		state.routes = append(state.routes, row)
	}

	return state, nil
}

// TeardownGatewayRoutes removes the /1 routes added by SetupGatewayRoutes.
func TeardownGatewayRoutes(state *RouteState) error {
	if state == nil {
		return nil
	}

	var errs []error
	for _, row := range state.routes {
		if err := row.Delete(); err != nil {
			errs = append(errs, fmt.Errorf("del route: %w", err))
		}
	}
	state.routes = nil

	if len(errs) > 0 {
		return fmt.Errorf("teardown errors: %v", errs)
	}
	return nil
}
