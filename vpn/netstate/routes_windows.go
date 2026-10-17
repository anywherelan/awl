//go:build windows

package netstate

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const (
	wfpClientSublayerName = "awl-gateway-client"
	// wfpClientRulePrefix names every fence rule; the hostnet tests look
	// rules up by this prefix.
	wfpClientRulePrefix = "awl-gateway-client: "

	// Weights of the fence rules within our sublayer (higher wins). The
	// permits must all outrank the block; the gaps leave room for future
	// rules. (The sublayer's own weight is wfpSublayerWeight, shared with the
	// server filter — see nat_windows.go.)
	fenceWeightPermitApp   = 4000
	fenceWeightPermitTun   = 3000
	fenceWeightPermitLocal = 2000
	fenceWeightBlock       = 1000
)

// routeState holds the state needed to teardown gateway routes on Windows.
type routeState struct {
	tunLUID winipcfg.LUID
	routes  []winipcfg.MibIPforwardRow2
	// wfpSession holds the leak fence (see setupClientFence). Dynamic: the
	// kernel removes its sublayer and rules when the session closes or the
	// process dies.
	wfpSession *wf.Session
}

// setupGatewayRoutes configures the gateway client routes on Windows:
//
//   - 0.0.0.0/1 + 128.0.0.0/1 via the TUN. More specific than the existing /0
//     default, so they win longest-prefix-match without replacing it.
//   - ::/1 + 8000::/1 via the TUN — the IPv6 fail-closed fence. awl does not
//     tunnel IPv6 (HandleReadPackets drops IsIPv6), so captured v6 packets die
//     in userspace instead of leaking around the gateway. Installed
//     unconditionally rather than "if v6 connectivity exists": both the v6
//     address and the ::/0 default arrive from RA (SLAAC), so at enable time
//     they may not exist yet and appear seconds later — a presence check
//     would be fail-open. If route creation fails because the TUN adapter
//     simply has no IPv6 stack (disabled system-wide), that is tolerated with
//     a warning; any other failure fails the setup (fail-closed).
//
// Before any route goes in, the WFP leak fence goes up (setupClientFence):
// fail-closed ordering, so there is never a window where the /1 capture is
// active but pre-existing connections may keep bypassing it.
//
// Crash semantics: all these routes are bound to the TUN LUID, so after
// kill -9 they die with the Wintun adapter — fail-open until restart for both
// families (on Linux the v6 fence survives a crash; Windows is weaker here,
// documented in GATEWAY_FEATURE.md). The WFP fence dies with the process too
// (dynamic session), consistently with the routes.
func (m *Manager) setupGatewayRoutes(tunIfName string) (*routeState, error) {
	luid, err := luidFromGUIDName(tunIfName)
	if err != nil {
		return nil, fmt.Errorf("resolve TUN interface: %w", err)
	}

	state := &routeState{
		tunLUID: luid,
	}

	if err := setupClientFence(state); err != nil {
		_ = m.teardownGatewayRoutes(state)
		return nil, fmt.Errorf("setup leak fence: %w", err)
	}

	v4Prefixes := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/1"),
		netip.MustParsePrefix("128.0.0.0/1"),
	}
	for _, prefix := range v4Prefixes {
		if err := state.addTunRoute(prefix, netip.IPv4Unspecified()); err != nil {
			_ = m.teardownGatewayRoutes(state)
			return nil, err
		}
	}

	v6Prefixes := []netip.Prefix{
		netip.MustParsePrefix("::/1"),
		netip.MustParsePrefix("8000::/1"),
	}
	for _, prefix := range v6Prefixes {
		err := state.addTunRoute(prefix, netip.IPv6Unspecified())
		if err == nil {
			continue
		}
		if !tunHasIPv6(luid) {
			// The same condition that makes setInterfaceMTU(AF_INET6) fail at
			// TUN creation: no v6 stack on the adapter means no v6 to fence.
			logger.Warnf("skipping IPv6 fail-closed fence: IPv6 appears disabled on the TUN adapter (%v)", err)
			break
		}
		// v6 exists on the adapter but the fence could not be installed —
		// continuing would silently leak IPv6 around the gateway.
		_ = m.teardownGatewayRoutes(state)
		return nil, fmt.Errorf("install IPv6 fail-closed fence: %w", err)
	}

	return state, nil
}

// addTunRoute creates one on-link route (unspecified next hop) via the TUN
// and records it for teardown.
func (state *routeState) addTunRoute(prefix netip.Prefix, nextHop netip.Addr) error {
	row := winipcfg.MibIPforwardRow2{}
	// InitializeIpForwardEntry is mandatory before CreateIpForwardEntry2
	// it sets ValidLifetime/PreferredLifetime to infinite and Protocol to NetMgmt.
	// Without it the route lands in the table with zero lifetimes and is
	// ignored by the forwarding path — visible in Get-NetRoute, yet traffic
	// keeps flowing past it.
	row.Init()
	row.InterfaceLUID = state.tunLUID
	row.DestinationPrefix.PrefixLength = uint8(prefix.Bits())
	if err := row.DestinationPrefix.RawPrefix.SetAddr(prefix.Addr()); err != nil {
		return fmt.Errorf("set destination prefix %s: %w", prefix, err)
	}
	// NextHop: unspecified addr = on-link (point-to-point TUN)
	if err := row.NextHop.SetAddr(nextHop); err != nil {
		return fmt.Errorf("set next hop for %s: %w", prefix, err)
	}
	row.Metric = 5 // low metric = high priority

	if err := row.Create(); err != nil {
		return fmt.Errorf("add route %s: %w", prefix, err)
	}
	state.routes = append(state.routes, row)
	return nil
}

// tunHasIPv6 reports whether the adapter has an IPv6 interface row — absent
// when IPv6 is disabled on the adapter or system-wide.
func tunHasIPv6(luid winipcfg.LUID) bool {
	_, err := luid.IPInterface(windows.AF_INET6)
	return err == nil
}

// teardownGatewayRoutes removes the routes added by setupGatewayRoutes, then
// closes the leak fence session last — the protection is dropped only once
// the /1 capture is gone (mirror of the fail-closed setup order). Blocked
// pre-gateway flows resume direct connectivity at that point; this is the
// same accepted disable-time fail-open as on Linux.
func (m *Manager) teardownGatewayRoutes(state *routeState) error {
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

	if state.wfpSession != nil {
		if err := state.wfpSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close leak fence WFP session: %w", err))
		}
		state.wfpSession = nil
	}

	return errors.Join(errs...)
}

// setupClientFence installs the WFP "leak fence" for gateway client mode.
//
// Why it exists: the /1 routes capture only traffic whose route is looked up
// fresh. Windows' strong host send model constrains the lookup for a socket
// with an already-fixed source address to the interface owning that address —
// so connections established BEFORE the gateway was enabled (browser
// HTTP/2/HTTP/3 keep-alive pools are the prime case) keep flowing directly
// through the physical NIC for as long as they live, invisible to the TUN
// routes. On Linux the weak host model routes those flows into the tunnel
// where they die (src is not NATed by the exit) and applications re-dial
// through the tunnel; on Windows nothing kills them — a real leak that
// routing cannot fix. The industry answer is a WFP firewall (userspace
// WireGuard's EnableFirewall), and this is our scoped version of it.
//
// Mechanism: one BLOCK on FWPM_LAYER_ALE_AUTH_CONNECT_V4/V6 with permits above
// it (see clientFenceRules). Adding a filter to the ALE connect layers makes
// WFP REAUTHORIZE established flows — the next outbound packet of a
// pre-gateway connection is reclassified and hits the BLOCK, so those flows
// die quickly and applications re-establish through the tunnel, matching the
// Linux behaviour. The v6 rules also close the tail the route-based v6 fence
// cannot: established IPv6 connections.
//
// Deliberately NOT blocked: inbound (no ALE_AUTH_RECV_ACCEPT filters).
// Established inbound flows to services hosted on this machine keep answering
// directly past the tunnel — a conscious divergence from Linux (where such
// replies die in the tunnel) so that enabling the gateway over an RDP session
// does not cut the session. Documented in README.
//
// The session is dynamic and separate from the server NAT's one (independent
// lifecycles; both roles can be active at once): the kernel removes
// everything when the session closes or the process dies, so the fence can
// never outlive the gateway — crash fail-open is consistent with the /1
// routes dying with the Wintun adapter.
func setupClientFence(state *routeState) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable: %w", err)
	}
	appID, err := wf.AppID(exe)
	if err != nil {
		return fmt.Errorf("resolve own WFP app ID: %w", err)
	}

	session, sublayerID, err := openDynamicWFPSession(
		"Anywherelan VPN gateway client fence",
		"Blocks traffic bypassing the VPN gateway tunnel (pre-existing connections, NIC-bound sockets)",
		wfpClientSublayerName,
	)
	if err != nil {
		return err
	}
	// Parked in state immediately so the caller's rollback closes the session
	// on any later failure — same idiom as the server-side setupWFP.
	state.wfpSession = session

	rules, err := clientFenceRules(sublayerID, state.tunLUID, appID)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := session.AddRule(rule); err != nil {
			return fmt.Errorf("add WFP rule %q: %w", rule.Name, err)
		}
	}
	return nil
}

// clientFenceRules builds the fence rule set for both address families: three
// PERMITs over one unconditional BLOCK per layer (within a sublayer the
// highest-weight matching filter wins).
//
//   - permit our own process (ALE_APP_ID): libp2p and the SOCKS5 exit-node
//     dials are bound to the uplink NIC by design (sockmark) and must keep
//     bypassing the tunnel. This does not let awl's unmarked sockets (the DNS
//     resolver's upstream) leak — a permit does not change routing, and those
//     still route into the TUN.
//   - permit tunnel egress (IP_LOCAL_INTERFACE == TUN): everything the /1
//     routes captured, for all applications.
//   - permit local destinations: loopback (the awl DNS resolver lives on
//     127.0.0.66), the private/CGNAT/link-local set (parity with Linux, where
//     on-link LAN routes legitimately beat the /1 capture — this also covers
//     DHCP renewals), multicast and broadcast (mDNS/SSDP/DHCP DISCOVER).
//     Multiple conditions on one field OR together.
//   - block everything else — most notably flows whose local interface is the
//     physical NIC: pre-gateway established connections and sockets
//     explicitly bound to the NIC address.
func clientFenceRules(sublayer wf.SublayerID, tunLUID winipcfg.LUID, appID string) ([]*wf.Rule, error) {
	localDst4 := slices.Concat(
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		privateSubnetPrefixes(),
		[]netip.Prefix{
			netip.MustParsePrefix("224.0.0.0/4"),
			netip.MustParsePrefix("255.255.255.255/32"),
		},
	)
	localDst6 := []netip.Prefix{
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("ff00::/8"),
	}

	layers := []struct {
		layer    wf.LayerID
		suffix   string
		localDst []netip.Prefix
	}{
		{wf.LayerALEAuthConnectV4, "v4", localDst4},
		{wf.LayerALEAuthConnectV6, "v6", localDst6},
	}

	var rules []*wf.Rule
	addRule := func(name string, layer wf.LayerID, weight uint64, conditions []*wf.Match, action wf.Action) error {
		id, err := newWFPGUID()
		if err != nil {
			return err
		}
		rules = append(rules, &wf.Rule{
			ID:         wf.RuleID(id),
			Name:       wfpClientRulePrefix + name,
			Layer:      layer,
			Sublayer:   sublayer,
			Weight:     weight,
			Conditions: conditions,
			Action:     action,
		})
		return nil
	}

	for _, l := range layers {
		err := addRule("permit awl process "+l.suffix, l.layer, fenceWeightPermitApp, []*wf.Match{
			{Field: wf.FieldALEAppID, Op: wf.MatchTypeEqual, Value: appID},
		}, wf.ActionPermit)
		if err != nil {
			return nil, err
		}
		err = addRule("permit tunnel egress "+l.suffix, l.layer, fenceWeightPermitTun, []*wf.Match{
			{Field: wf.FieldIPLocalInterface, Op: wf.MatchTypeEqual, Value: uint64(tunLUID)},
		}, wf.ActionPermit)
		if err != nil {
			return nil, err
		}
		var localConds []*wf.Match
		for _, p := range l.localDst {
			localConds = append(localConds, &wf.Match{
				Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: p,
			})
		}
		err = addRule("permit local destinations "+l.suffix, l.layer, fenceWeightPermitLocal, localConds, wf.ActionPermit)
		if err != nil {
			return nil, err
		}
		err = addRule("block tunnel bypass "+l.suffix, l.layer, fenceWeightBlock, nil, wf.ActionBlock)
		if err != nil {
			return nil, err
		}
	}
	return rules, nil
}

// luidFromGUIDName resolves an interface name as used by awl on Windows — a
// GUID string like "{13b1820f-...}" (see vpn.Device.InterfaceName) — into a
// winipcfg.LUID. Fails if the adapter does not currently exist.
//
// The LUID is technically already known where the TUN is created
// (vpn/iface_windows.go, NativeTun.LUID()), but the routes/NAT API carries
// the interface identity as a platform-neutral string ("awl0" on Linux, the
// GUID here) through cross-platform service code, so we convert back. That's
// two cheap lookups (ConvertInterfaceGuidToLuid) once per gateway enable —
// cheaper than windows-only plumbing through service/vpn_gateway.go.
func luidFromGUIDName(ifName string) (winipcfg.LUID, error) {
	guid, err := windows.GUIDFromString(ifName)
	if err != nil {
		return 0, fmt.Errorf("parse interface GUID %q: %w", ifName, err)
	}
	luid, err := winipcfg.LUIDFromGUID(&guid)
	if err != nil {
		return 0, fmt.Errorf("resolve LUID for %s: %w", ifName, err)
	}
	return luid, nil
}
