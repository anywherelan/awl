//go:build linux && !android

package routes

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	// tableID is the policy-routing table that holds the original main-table
	// default route(s), so fwmark-tagged libp2p sockets can still reach the
	// physical NIC while everything else is forced through the TUN.
	//
	// 0x61776C = "awl" in ASCII (lowercase). The sockmark package uses the
	// same numeric value as the fwmark; the two live in different kernel
	// namespaces and don't collide, but matching them makes awl-owned state
	// trivially greppable in `ip rule` / `ip route show table`.
	tableID = 0x61776C

	// rulePriority places our fwmark→tableID rule before the main table
	// (priority 32766). 32000 leaves room for wg-quick's conventional 32764
	// in case both run on the same host.
	rulePriority = 32000

	// tunRouteMetric is the metric for the default route we add via the TUN.
	// Must beat typical NetworkManager defaults (100 wired / 600 wireless)
	// and systemd-networkd's 1024 so that LPM picks our route. Not 0 — leaves
	// the very-low band free for user-managed special routes.
	tunRouteMetric = 5
)

// RouteState holds the state needed to teardown gateway routes.
type RouteState struct {
	tunLinkIndex int
	fwmark       uint32

	// origDefaults are the IPv4 default routes that existed before SetupGateway
	// was called. They are NOT mutated; the new TUN default route is added
	// alongside via RouteAdd, so teardown only has to remove what we added
	// instead of restoring originals.
	origDefaults  []netlink.Route
	tunRouteAdded bool

	// IPv6 fail-closed state. The gateway only tunnels IPv4; to stop IPv6 from
	// leaking past the exit node on a dual-stack host we fence it with an
	// `unreachable ::/0` route, and exempt marked libp2p sockets via a v6
	// fwmark rule + a copy of the host's IPv6 default(s) into tableID. Mirrors
	// the IPv4 fields above. origDefaultsV6 may be empty (host has no IPv6).
	origDefaultsV6 []netlink.Route
	v6RuleAdded    bool
	v6UnreachAdded bool
}

// SetupGatewayRoutes configures the system to route all traffic through the
// TUN interface, while exempting marked (libp2p) sockets via policy routing.
//
// Steps:
//  1. Snapshot the existing IPv4 default routes (for reporting and to copy
//     them into the policy-routing table).
//  2. Add an ip rule: fwmark → tableID.
//  3. Add the original default route(s) into tableID so marked libp2p
//     sockets reach the physical interface.
//  4. Add a default route via the TUN with a *lower metric* so it wins LPM
//     selection; teardown only deletes this added route, leaving the
//     pre-existing defaults untouched.
//
// If a previous run was killed before TeardownGatewayRoutes could complete,
// the rule and tableID entries may still be present; cleanupStaleRoutes
// removes those leftovers best-effort before we proceed. The TUN default
// route itself is not subject to spec-cleanup — see cleanupStaleRoutes.
func SetupGatewayRoutes(tunIfName string, fwmark uint32) (*RouteState, error) {
	tunLink, err := netlink.LinkByName(tunIfName)
	if err != nil {
		return nil, fmt.Errorf("find TUN interface %s: %w", tunIfName, err)
	}

	// Best-effort removal of leftovers from a previous run. Done before we
	// snapshot original defaults so that the leftover tableID contents don't
	// mask the user's true main-table state.
	if cleaned := cleanupStaleRoutes(fwmark); cleaned {
		logger.Warnf("recovered from leftover gateway route state (previous run was likely killed before teardown)")
	}

	// TODO(gateway-route-staleness): origDefaults (and origDefaultsV6 below) are
	// snapshotted once here and copied into tableID; they are never refreshed.
	// Applies to BOTH families. If the host default changes mid-session (IPv4:
	// DHCP renew, Wi-Fi<->Ethernet roaming; IPv6: RA re-advertising a new
	// router/prefix — far more frequent), the copy in tableID goes stale and
	// marked libp2p sockets lose their physical-NIC exit until the gateway is
	// re-toggled. This does NOT cause a leak — the catch-all TUN default / v6
	// unreachable route is static — only degraded p2p connectivity. Fix:
	// subscribe to netlink RTM_NEWROUTE/RTM_DELROUTE and re-copy the live
	// default(s) into tableID for both families.
	origDefaults, err := getDefaultRoutes()
	if err != nil {
		return nil, fmt.Errorf("get default routes: %w", err)
	}
	if len(origDefaults) == 0 {
		return nil, fmt.Errorf("no IPv4 default route present, cannot configure VPN gateway")
	}

	state := &RouteState{
		tunLinkIndex: tunLink.Attrs().Index,
		fwmark:       fwmark,
		origDefaults: origDefaults,
	}

	// 1. Add ip rule: marked packets use tableID.
	if err := netlink.RuleAdd(buildFwmarkRule(fwmark)); err != nil {
		return nil, fmt.Errorf("add ip rule: %w", err)
	}

	// 2. Copy each original default route into tableID so that marked
	// libp2p traffic still reaches the physical NIC.
	for i := range origDefaults {
		tableRoute := origDefaults[i]
		tableRoute.Table = tableID
		if err := netlink.RouteAdd(&tableRoute); err != nil {
			_ = TeardownGatewayRoutes(state)
			return nil, fmt.Errorf("add original default to table %d: %w", tableID, err)
		}
	}

	// 3. Add a default route via TUN with a low metric, leaving existing
	// defaults intact. RouteReplace would clobber multi-NIC setups
	// (Wi-Fi + Ethernet); RouteAdd preserves them.
	//
	// EEXIST here means either a leftover from a prior awl run that escaped
	// our cleanup, or someone else's default with the same metric. We don't
	// try to delete it ourselves — metric is not a reliable owner-tag
	// (dhclient/OpenVPN/admin scripts can all land on similar values), so
	// we surface a clear diagnostic and let the operator resolve it.
	if err := netlink.RouteAdd(buildTunDefaultRoute(tunLink.Attrs().Index)); err != nil {
		_ = TeardownGatewayRoutes(state)
		if errors.Is(err, syscall.EEXIST) {
			return nil, fmt.Errorf("add TUN default route: %w — possible leftover from a prior awl run, "+
				"inspect with `ip route show default` and remove with `ip route del default metric %d` if it is stale",
				err, tunRouteMetric)
		}
		return nil, fmt.Errorf("add TUN default route: %w", err)
	}
	state.tunRouteAdded = true

	// IPv6 fail-closed fence (`unreachable ::/0` + libp2p exemption). Installed
	// unconditionally — see setupIPv6Fence for why that is safe even when IPv6 is
	// disabled via sysctl, and how a genuinely absent IPv6 stack is tolerated.
	if err := setupIPv6Fence(state, fwmark); err != nil {
		_ = TeardownGatewayRoutes(state)
		return nil, err
	}

	return state, nil
}

// setupIPv6Fence installs the IPv6 fail-closed fencing onto state: a v6
// fwmark->tableID rule, a copy of the host's current IPv6 default(s) into
// tableID (the libp2p exemption path), and an `unreachable ::/0` route that wins
// LPM over any host default so locally generated IPv6 connect()s fail fast with
// EHOSTUNREACH and apps fall back to IPv4 through the tunnel (Happy Eyeballs,
// RFC 8305). Without it a dual-stack host egresses IPv6 straight out its
// physical interface, exposing the real address past the exit node.
//
// It is applied UNCONDITIONALLY, even when the host has no IPv6 default right
// now. The unreachable route and the rule install fine even with IPv6
// administratively disabled via sysctl (`disable_ipv6=1` only blocks address
// assignment, not route/rule additions — verified on Linux 6.8). Installing it
// regardless means IPv6 that appears later — a hot-plugged uplink, a runtime
// sysctl flip, a fresh RA — is already fenced and loses LPM to our metric-5
// unreachable, rather than leaking. An empty IPv6 default set is therefore not
// an error (unlike the IPv4 default above).
//
// The one case where the IPv6 stack genuinely isn't there is a kernel-level
// disable (`ipv6.disable=1` on the cmdline): the module is absent, AF_INET6 ops
// fail with EAFNOSUPPORT, and there is nothing to leak. We detect that from the
// netlink ops and skip the fence (leaving state.v6* unset) rather than failing
// the otherwise-working IPv4 gateway setup.
func setupIPv6Fence(state *RouteState, fwmark uint32) error {
	origDefaultsV6, err := getDefaultRoutesV6()
	if err != nil {
		if ipv6Unavailable(err) {
			logger.Infof("IPv6 stack unavailable (%v); skipping IPv6 leak fence", err)
			return nil
		}
		return fmt.Errorf("get IPv6 default routes: %w", err)
	}

	if err := netlink.RuleAdd(buildFwmarkRuleV6(fwmark)); err != nil {
		if ipv6Unavailable(err) {
			logger.Infof("IPv6 stack unavailable (%v); skipping IPv6 leak fence", err)
			return nil
		}
		return fmt.Errorf("add IPv6 ip rule: %w", err)
	}
	state.v6RuleAdded = true
	state.origDefaultsV6 = origDefaultsV6

	for i := range origDefaultsV6 {
		tableRoute := origDefaultsV6[i]
		tableRoute.Table = tableID
		if err := netlink.RouteAdd(&tableRoute); err != nil {
			return fmt.Errorf("add original IPv6 default to table %d: %w", tableID, err)
		}
	}

	if err := netlink.RouteAdd(buildV6UnreachableRoute()); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("add IPv6 unreachable default route: %w — a ::/0 route at metric %d "+
				"already exists (likely another VPN or a manual route, not awl: cleanupStaleRoutes "+
				"already removed any of ours); inspect with `ip -6 route show` and resolve the conflict",
				err, tunRouteMetric)
		}
		return fmt.Errorf("add IPv6 unreachable default route: %w", err)
	}
	state.v6UnreachAdded = true

	return nil
}

// ipv6Unavailable reports whether a netlink error means the IPv6 stack is not
// present at all (kernel-level ipv6.disable=1), as opposed to a real failure to
// be surfaced. When it is, there is nothing to fence.
func ipv6Unavailable(err error) bool {
	return errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT)
}

// cleanupStaleRoutes removes leftover state from a previous SetupGatewayRoutes
// call: the fwmark→tableID ip rule and every route currently in tableID. All
// errors are intentionally swallowed — this is a best-effort pre-clean before
// the real adds, not a fully accountable teardown.
//
// We deliberately do NOT try to clean a stale TUN default route here:
//
//   - awl uses a userspace TUN created via /dev/net/tun, which is tied to the
//     process's fd. When the process dies (even via SIGKILL), the kernel
//     destroys the interface and auto-removes every route pointing to it.
//     An orphan TUN default route is therefore extremely unlikely.
//
//   - We have no reliable owner-tag for the route. Filtering by metric would
//     risk deleting routes added by dhclient (which often lands at metric 0
//     or low values), OpenVPN (push "route-metric 5" is a common config),
//     or a system administrator's static route. Better to surface a clear
//     error from RouteAdd's EEXIST than silently delete someone else's
//     traffic path.
func cleanupStaleRoutes(fwmark uint32) bool {
	cleaned := false

	// 1. Stale ip rule: fwmark → tableID.
	if err := netlink.RuleDel(buildFwmarkRule(fwmark)); err == nil {
		cleaned = true
	}

	// 2. Every route currently in tableID. We own the table by convention
	// (its value is "awl" in ASCII), so anything inside it is leftover.
	// Filter on Table only — LinkIndex of the original routes may differ
	// from run to run if the physical NIC was renumbered. Both families share
	// the table, so sweep it for v4 and v6.
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routesInTable, err := netlink.RouteListFiltered(family,
			&netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue
		}
		for i := range routesInTable {
			r := routesInTable[i]
			if delErr := netlink.RouteDel(&r); delErr == nil {
				cleaned = true
			}
		}
	}

	// 3. Stale IPv6 fwmark rule and the `unreachable ::/0` fence. Unlike the
	// IPv4 TUN default route (which the kernel auto-removes when the TUN fd dies
	// with the process), the v6 unreachable route is not bound to any interface,
	// so a SIGKILL'd run leaves it behind — fencing off IPv6 host-wide until it
	// is removed. It IS owner-tagged (its low metric + ::/0 + RTN_UNREACHABLE
	// shape is ours), so we clean it here rather than surfacing an EEXIST.
	if err := netlink.RuleDel(buildFwmarkRuleV6(fwmark)); err == nil {
		cleaned = true
	}
	if err := netlink.RouteDel(buildV6UnreachableRoute()); err == nil {
		cleaned = true
	}

	return cleaned
}

// TeardownGatewayRoutes reverses the changes made by SetupGatewayRoutes.
func TeardownGatewayRoutes(state *RouteState) error {
	if state == nil {
		return nil
	}

	var errs []error

	// Remove the TUN default route we added.
	if state.tunRouteAdded {
		if err := netlink.RouteDel(buildTunDefaultRoute(state.tunLinkIndex)); err != nil {
			errs = append(errs, fmt.Errorf("del TUN default route: %w", err))
		}
	}

	// Remove every default we copied into the auxiliary table.
	for i := range state.origDefaults {
		tableRoute := state.origDefaults[i]
		tableRoute.Table = tableID
		if err := netlink.RouteDel(&tableRoute); err != nil {
			errs = append(errs, fmt.Errorf("del route from table %d: %w", tableID, err))
		}
	}

	if err := netlink.RuleDel(buildFwmarkRule(state.fwmark)); err != nil {
		errs = append(errs, fmt.Errorf("del ip rule: %w", err))
	}

	// IPv6 fail-closed teardown, reverse order of setup: unreachable fence,
	// copied defaults, then the v6 fwmark rule. Guarded by the per-step flags so
	// a rollback from a partially-applied setup doesn't generate spurious errors.
	if state.v6UnreachAdded {
		if err := netlink.RouteDel(buildV6UnreachableRoute()); err != nil {
			errs = append(errs, fmt.Errorf("del IPv6 unreachable default route: %w", err))
		}
	}
	for i := range state.origDefaultsV6 {
		tableRoute := state.origDefaultsV6[i]
		tableRoute.Table = tableID
		if err := netlink.RouteDel(&tableRoute); err != nil {
			errs = append(errs, fmt.Errorf("del IPv6 route from table %d: %w", tableID, err))
		}
	}
	if state.v6RuleAdded {
		if err := netlink.RuleDel(buildFwmarkRuleV6(state.fwmark)); err != nil {
			errs = append(errs, fmt.Errorf("del IPv6 ip rule: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("teardown gateway routes: %w", errors.Join(errs...))
	}
	return nil
}

// buildFwmarkRule constructs the ip rule used to steer fwmark-tagged packets
// into tableID. Same shape is used for Add, Del (cleanup), and Del (teardown)
// so they can't drift.
func buildFwmarkRule(fwmark uint32) *netlink.Rule {
	r := netlink.NewRule()
	r.Mark = fwmark
	r.Table = tableID
	r.Priority = rulePriority
	r.Family = netlink.FAMILY_V4
	return r
}

// buildFwmarkRuleV6 is the IPv6 counterpart of buildFwmarkRule: it steers
// fwmark-tagged IPv6 packets (libp2p sockets) into tableID so they reach the
// physical NIC instead of hitting the `unreachable ::/0` fence. SO_MARK is set
// on every socket regardless of family, so the same fwmark value applies.
func buildFwmarkRuleV6(fwmark uint32) *netlink.Rule {
	r := netlink.NewRule()
	r.Mark = fwmark
	r.Table = tableID
	r.Priority = rulePriority
	r.Family = netlink.FAMILY_V6
	return r
}

// buildTunDefaultRoute constructs the default route via the TUN. Scope is
// SCOPE_LINK because the TUN is a point-to-point device with no gateway —
// this matches what `ip route add default dev awl0` would produce, and using
// the identical shape on RouteDel is required for the kernel to match it.
//
// Dst must be an explicit 0.0.0.0/0 *net.IPNet rather than nil: netlink's
// RouteAdd rejects a route with no Dst.IP, Src and Gw ("either Dst.IP, Src.IP
// or Gw must be set"). A TUN default route has no gateway (point-to-point), so
// the destination is the only field we can populate to satisfy that check.
func buildTunDefaultRoute(tunLinkIndex int) *netlink.Route {
	return &netlink.Route{
		LinkIndex: tunLinkIndex,
		Dst: &net.IPNet{
			IP:   net.IPv4zero,
			Mask: net.CIDRMask(0, 32),
		},
		Scope:    netlink.SCOPE_LINK,
		Priority: tunRouteMetric,
	}
}

// buildV6UnreachableRoute constructs the `unreachable ::/0` fence installed
// while the gateway is on. RTN_UNREACHABLE (not RTN_BLACKHOLE) so locally
// generated IPv6 connect()s fail fast with EHOSTUNREACH and apps fall back to
// IPv4 through the tunnel (Happy Eyeballs, RFC 8305) instead of timing out.
// Same low metric as the IPv4 TUN default so it wins LPM over the host's
// RA/DHCPv6 default. The identical shape is used for Add, stale-cleanup Del and
// teardown Del so they can't drift. No LinkIndex: an unreachable route is not
// attached to any interface.
func buildV6UnreachableRoute() *netlink.Route {
	return &netlink.Route{
		Type: unix.RTN_UNREACHABLE,
		Dst: &net.IPNet{
			IP:   net.IPv6zero,
			Mask: net.CIDRMask(0, 128),
		},
		Priority: tunRouteMetric,
		Family:   netlink.FAMILY_V6,
	}
}

// getDefaultRoutes returns every IPv4 default route currently in the main
// routing table. Hosts with multiple uplinks (Wi-Fi + Ethernet) typically
// have several; we copy all of them into the policy-routing table.
func getDefaultRoutes() ([]netlink.Route, error) {
	allRoutes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}

	var defaults []netlink.Route
	for i := range allRoutes {
		r := allRoutes[i]
		if !isIPv4DefaultDst(r.Dst) {
			continue
		}
		defaults = append(defaults, r)
	}
	return defaults, nil
}

// isIPv4DefaultDst reports whether dst represents the IPv4 default route
// (nil, or 0.0.0.0/0 expressed as a *net.IPNet with a /0 mask).
func isIPv4DefaultDst(dst *net.IPNet) bool {
	if dst == nil {
		return true
	}
	if !dst.IP.Equal(net.IPv4zero) {
		return false
	}
	bits, _ := dst.Mask.Size()
	return bits == 0
}

// getDefaultRoutesV6 returns every IPv6 default route (::/0) currently in the
// main routing table, to be copied into tableID as the libp2p exemption path.
// Unlike getDefaultRoutes, an empty result is NOT an error: the gateway installs
// the `unreachable ::/0` fence unconditionally, so a host with no IPv6 uplink is
// simply fenced against IPv6 that may appear later via RA.
func getDefaultRoutesV6() ([]netlink.Route, error) {
	allRoutes, err := netlink.RouteList(nil, netlink.FAMILY_V6)
	if err != nil {
		return nil, fmt.Errorf("list IPv6 routes: %w", err)
	}

	var defaults []netlink.Route
	for i := range allRoutes {
		r := allRoutes[i]
		if !isIPv6DefaultDst(r.Dst) {
			continue
		}
		defaults = append(defaults, r)
	}
	return defaults, nil
}

// isIPv6DefaultDst reports whether dst represents the IPv6 default route
// (nil, or ::/0 expressed as a *net.IPNet with a /0 mask).
func isIPv6DefaultDst(dst *net.IPNet) bool {
	if dst == nil {
		return true
	}
	if !dst.IP.Equal(net.IPv6zero) {
		return false
	}
	bits, _ := dst.Mask.Size()
	return bits == 0
}
