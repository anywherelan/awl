//go:build linux && vpn_hostnet

// Package routes host-network integration tests.
//
// These tests exercise the real Linux netfilter / netlink plumbing
// (SetupNAT/TeardownNAT and SetupGatewayRoutes/TeardownGatewayRoutes) against
// the *actual* host network: they create a dummy `awl0` link, install ip rules,
// iptables chains and routes, and assert they are applied and then fully torn
// down.
//
// They are DANGEROUS by nature — while a test is mid-flight the host has a
// default route pointed at a dead dummy interface, i.e. its egress is a black
// hole until teardown runs. Each test tears down in the same body, but if one
// panics the host may be left with stale state. For that reason they are:
//
//   - hidden behind the `vpn_hostnet` build tag (excluded from `go test ./...`),
//   - Linux-only (`//go:build linux`),
//   - require root (CAP_NET_ADMIN) — they fail loudly otherwise (the build tag
//     already prevents accidental runs, so a non-root run is a misconfiguration,
//     not something to silently skip).
//
// Run them via a compiled binary so root never touches the Go build cache:
//
//	go test -c -tags vpn_hostnet -o gw-hostnet.test ./vpn/routes/
//	sudo ./gw-hostnet.test -test.run '^TestGatewayHostNet' -test.v
//
// A dummy link (not a real TUN) is enough: the production code only references
// the interface by name (`-i/-o awl0`, route oif). The one behavioural
// difference — a real userspace TUN's routes self-destruct when the process
// dies, a dummy's do not — is never relied upon here: we always either tear
// down explicitly or delete the link to simulate that self-destruction.
package routes

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"

	"github.com/anywherelan/awl/vpn/sockmark"
)

const (
	testTunIf     = "awl0"
	testAwlSubnet = "10.66.0.0/16"
	ipForwardPath = "/proc/sys/net/ipv4/ip_forward"
)

func testFWMark() uint32 { return sockmark.New().FWMark() }

// ---- N1: NAT apply/teardown lifecycle ----

func TestGatewayHostNetNATLifecycle(t *testing.T) {
	requireRoot(t)
	setupDummyTun(t)
	origForward := captureForward(t)

	before := snapshotNet(t)

	state, err := SetupNAT(testAwlSubnet, testTunIf)
	require.NoError(t, err)

	assertNATApplied(t)
	require.Equal(t, "1", readForward(t), "ip_forward must be on while NAT is up")

	require.NoError(t, TeardownNAT(state))

	require.Equal(t, before, snapshotNet(t), "teardown must restore the exact pre-setup netfilter state")
	require.Equal(t, origForward, readForward(t),
		"a single setup/teardown must restore ip_forward to its original value")
}

// ---- N2: NAT re-setup is idempotent (kill -9 recovery) ----
//
// A second SetupNAT without a teardown in between is exactly what happens after
// a crash: cleanupStaleNAT must recover the leftover scaffolding so NewChain
// succeeds and the resulting state is identical to a single clean setup.

func TestGatewayHostNetNATIdempotentResetup(t *testing.T) {
	requireRoot(t)
	setupDummyTun(t)
	captureForward(t)

	before := snapshotNet(t)

	_, err := SetupNAT(testAwlSubnet, testTunIf)
	require.NoError(t, err)
	applied1 := snapshotNet(t)

	// Second setup over the live state of the first — simulates a leftover from
	// a process that was killed before TeardownNAT ran.
	state2, err := SetupNAT(testAwlSubnet, testTunIf)
	require.NoError(t, err, "re-setup over leftover state must succeed (cleanupStaleNAT)")
	applied2 := snapshotNet(t)

	require.Equal(t, applied1, applied2, "re-setup must produce identical state, no duplicate rules")

	require.NoError(t, TeardownNAT(state2))
	require.Equal(t, before, snapshotNet(t), "single teardown must clean everything after a re-setup")
}

// ---- N3: NAT leaves a pre-existing ip_forward=1 untouched ----

func TestGatewayHostNetNATPreservesExistingIPForward(t *testing.T) {
	requireRoot(t)
	setupDummyTun(t)
	captureForward(t)

	if readForward(t) != "1" {
		t.Skip("ip_forward is not already on; this case needs a host where it was pre-enabled (e.g. Docker)")
	}

	state, err := SetupNAT(testAwlSubnet, testTunIf)
	require.NoError(t, err)
	require.Equal(t, "1", readForward(t))

	require.NoError(t, TeardownNAT(state))
	require.Equal(t, "1", readForward(t),
		"ip_forward was already on before setup; teardown must NOT reset it to 0")
}

// ---- R1: route apply/teardown lifecycle ----

func TestGatewayHostNetRoutesLifecycle(t *testing.T) {
	requireRoot(t)
	requireDefaultRoute(t)
	setupDummyTun(t)

	before := snapshotNet(t)

	state, err := SetupGatewayRoutes(testTunIf, testFWMark())
	require.NoError(t, err)

	assertRoutesApplied(t)

	require.NoError(t, TeardownGatewayRoutes(state))
	require.Equal(t, before, snapshotNet(t), "teardown must restore the exact pre-setup routing state")
}

// ---- R2: route stale-recovery after the TUN's own default self-destructed ----
//
// Deleting the link drops the kernel default route via it (mirrors a real TUN
// dying with its process), while the fwmark rule and the auxiliary-table copies
// are orphaned. A fresh SetupGatewayRoutes must clean those up (cleanupStaleRoutes)
// and succeed without an EEXIST on the new TUN default.

func TestGatewayHostNetRoutesStaleRecovery(t *testing.T) {
	requireRoot(t)
	requireDefaultRoute(t)
	setupDummyTun(t)

	before := snapshotNet(t)

	_, err := SetupGatewayRoutes(testTunIf, testFWMark())
	require.NoError(t, err)
	applied1 := snapshotNet(t)

	// Simulate the TUN dying: deleting awl0 makes the kernel auto-remove the
	// default route via it, leaving the fwmark rule + table copies orphaned.
	recreateDummyTun(t)

	state2, err := SetupGatewayRoutes(testTunIf, testFWMark())
	require.NoError(t, err, "re-setup must recover orphaned ip rule + table routes (cleanupStaleRoutes)")
	require.Equal(t, applied1, snapshotNet(t), "recovered state must match a clean single setup")

	require.NoError(t, TeardownGatewayRoutes(state2))
	require.Equal(t, before, snapshotNet(t))
}

// ---- R3: a leftover TUN default route is reported, not silently clobbered ----
//
// cleanupStaleRoutes deliberately never deletes the TUN default route (no
// reliable owner-tag; deleting by metric risks nuking dhclient/OpenVPN routes).
// So when the link did NOT go away (here: a still-present dummy with the route
// seeded), SetupGatewayRoutes must surface a clear diagnostic instead of
// duplicating or clobbering.

func TestGatewayHostNetRoutesLeftoverTunRouteErrors(t *testing.T) {
	requireRoot(t)
	requireDefaultRoute(t)
	setupDummyTun(t)

	before := snapshotNet(t)

	// Seed a leftover TUN default route with the exact shape the code uses, so
	// the subsequent RouteAdd collides with EEXIST.
	link, err := netlink.LinkByName(testTunIf)
	require.NoError(t, err)
	leftover := buildTunDefaultRoute(link.Attrs().Index)
	require.NoError(t, netlink.RouteAdd(leftover))
	t.Cleanup(func() { _ = netlink.RouteDel(leftover) })

	_, err = SetupGatewayRoutes(testTunIf, testFWMark())
	require.Error(t, err)
	require.Contains(t, err.Error(), "leftover from a prior awl run",
		"a colliding TUN default must produce the operator-facing diagnostic")

	require.NoError(t, netlink.RouteDel(leftover))
	require.Equal(t, before, snapshotNet(t), "the failed setup must leave no partial state behind")
}

// ---------------------------------------------------------------------------
// assertions
// ---------------------------------------------------------------------------

// assertNATApplied checks the AWL-FORWARD chain exists with the exact rule
// order, both FORWARD jumps are present, MASQUERADE is installed.
func assertNATApplied(t *testing.T) {
	t.Helper()

	want := []string{
		"-N " + awlForwardChain,
		"-A " + awlForwardChain + " -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
	}
	for _, p := range privateSubnets {
		want = append(want, "-A "+awlForwardChain+" -d "+p+" -j DROP")
	}
	want = append(want, "-A "+awlForwardChain+" -j ACCEPT")

	got := lines(cmdOut(t, "iptables", "-S", awlForwardChain))
	require.Equal(t, want, got, "AWL-FORWARD chain content/order")

	// iptables -S prints matches in its own canonical order (-s before -i,
	// -d before -o), regardless of the order the code passes them in
	// (outboundJumpArgs/returnJumpArgs use -i/-s and -o/-d), so assert against
	// that canonical form.
	filter := cmdOut(t, "iptables", "-S", "FORWARD")
	require.Contains(t, filter, "-s "+testAwlSubnet+" -i "+testTunIf+" -j "+awlForwardChain, "outbound jump")
	require.Contains(t, filter, "-d "+testAwlSubnet+" -o "+testTunIf+" -j "+awlForwardChain, "return jump")

	nat := cmdOut(t, "iptables", "-t", "nat", "-S", "POSTROUTING")
	require.Contains(t, nat, "-s "+testAwlSubnet+" ! -o "+testTunIf+" -j MASQUERADE", "MASQUERADE")
}

func assertRoutesApplied(t *testing.T) {
	t.Helper()

	rules := cmdOut(t, "ip", "rule", "show")
	require.Contains(t, rules, fmt.Sprintf("fwmark 0x%x", testFWMark()), "fwmark ip rule")
	require.Contains(t, rules, fmt.Sprintf("lookup %d", tableID), "ip rule must steer to the awl table")

	main := cmdOut(t, "ip", "-4", "route", "show")
	require.Contains(t, main, "default", "default route present")
	require.Contains(t, main, "dev "+testTunIf, "default must be via the TUN")
	require.Contains(t, main, fmt.Sprintf("metric %d", tunRouteMetric), "TUN default metric")

	table := strings.TrimSpace(cmdOut(t, "ip", "-4", "route", "show", "table", strconv.Itoa(tableID)))
	require.NotEmpty(t, table, "original default(s) must be copied into the awl table")

	// IPv6 fail-closed fence. Installed unconditionally — even with IPv6 disabled
	// via sysctl (disable_ipv6=1 blocks addresses, not routes). It is skipped
	// only when the IPv6 stack is absent entirely (kernel ipv6.disable=1 →
	// /proc/sys/net/ipv6 missing), matching setupIPv6Fence's EAFNOSUPPORT path.
	if _, err := os.Stat("/proc/sys/net/ipv6"); os.IsNotExist(err) {
		return
	}
	rules6 := cmdOut(t, "ip", "-6", "rule", "show")
	require.Contains(t, rules6, fmt.Sprintf("fwmark 0x%x", testFWMark()), "v6 fwmark ip rule")
	require.Contains(t, rules6, fmt.Sprintf("lookup %d", tableID), "v6 ip rule must steer to the awl table")

	// Anchor the metric to the unreachable line so an unrelated host route can't
	// satisfy it.
	main6 := cmdOut(t, "ip", "-6", "route", "show")
	require.Regexp(t, fmt.Sprintf(`unreachable default.*metric %d`, tunRouteMetric), main6,
		"IPv6 unreachable fence present at the expected metric")
}

// ---------------------------------------------------------------------------
// snapshot
// ---------------------------------------------------------------------------

// snapshotNet captures every piece of host network state SetupNAT/SetupGatewayRoutes
// can touch, EXCEPT ip_forward (which has its own preserve-if-on semantics and is
// asserted separately). Lines within each section are sorted so the comparison is
// about set membership, not iptables/ip print order.
func snapshotNet(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	section := func(title, out string) {
		ls := lines(out)
		sort.Strings(ls)
		b.WriteString("== " + title + " ==\n")
		b.WriteString(strings.Join(ls, "\n"))
		b.WriteString("\n")
	}
	section("ip rule", cmdOut(t, "ip", "rule", "show"))
	section("route main", cmdOut(t, "ip", "-4", "route", "show"))
	section("route awl-table", routeTableDump(t, tableID))
	// v6 route dumps are sanitized: RA-originated defaults carry an `expires
	// Nsec` countdown that ticks between snapshots and would make before/after
	// equality flaky on a dual-stack host.
	section("ip -6 rule", cmdOut(t, "ip", "-6", "rule", "show"))
	section("route6 main", stripVolatile(cmdOut(t, "ip", "-6", "route", "show")))
	section("route6 awl-table", stripVolatile(route6TableDump(t, tableID)))
	section("iptables filter", cmdOut(t, "iptables", "-S"))
	section("iptables nat", cmdOut(t, "iptables", "-t", "nat", "-S"))
	return b.String()
}

// volatileExpires matches the `expires Nsec` attribute that the kernel prints
// for RA-learned IPv6 routes; its countdown changes between snapshots.
var volatileExpires = regexp.MustCompile(`expires \d+sec`)

func stripVolatile(s string) string {
	return volatileExpires.ReplaceAllString(s, "expires")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("requires root (CAP_NET_ADMIN); build with -tags vpn_hostnet and run the binary via sudo")
	}
}

// requireDefaultRoute skips if the host has no IPv4 default route, since
// SetupGatewayRoutes legitimately refuses to run without one.
func requireDefaultRoute(t *testing.T) {
	t.Helper()
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	require.NoError(t, err)
	for _, r := range routes {
		if r.Dst == nil || (r.Dst.IP.IsUnspecified()) {
			return
		}
	}
	t.Skip("no IPv4 default route on this host; gateway routes cannot be configured")
}

func setupDummyTun(t *testing.T) {
	t.Helper()
	_ = exec.Command("ip", "link", "del", testTunIf).Run() // best-effort pre-clean
	mustCmd(t, "ip", "link", "add", testTunIf, "type", "dummy")
	mustCmd(t, "ip", "link", "set", testTunIf, "up")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", testTunIf).Run() })
}

func recreateDummyTun(t *testing.T) {
	t.Helper()
	mustCmd(t, "ip", "link", "del", testTunIf)
	mustCmd(t, "ip", "link", "add", testTunIf, "type", "dummy")
	mustCmd(t, "ip", "link", "set", testTunIf, "up")
}

func readForward(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(ipForwardPath)
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}

// captureForward records the current ip_forward value and restores it after the
// test, so tests stay order-independent regardless of the leave-if-on rule.
func captureForward(t *testing.T) string {
	t.Helper()
	orig := readForward(t)
	t.Cleanup(func() { _ = os.WriteFile(ipForwardPath, []byte(orig), 0o600) })
	return orig
}

func mustCmd(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	require.NoErrorf(t, err, "%s %s: %s", name, strings.Join(args, " "), out)
}

func cmdOut(t *testing.T, name string, args ...string) string {
	t.Helper()
	// CombinedOutput (not Output) so a failing command surfaces its stderr
	// diagnostic in the test log instead of a bare "exit status N". On success
	// these commands print nothing to stderr, so the captured value is unchanged.
	out, err := exec.Command(name, args...).CombinedOutput()
	require.NoErrorf(t, err, "%s %s: %s", name, strings.Join(args, " "), out)
	return string(out)
}

// routeTableDump returns the routes in the given table, tolerating the
// "table does not exist" case. Newer iproute2/kernels (e.g. Ubuntu 24.04) make
// `ip route show table <id>` fail with exit 2 ("FIB table does not exist") when
// the table has never held a route, whereas older versions returned empty with
// exit 0. Both mean the same thing here — an empty table — so normalise to "".
func routeTableDump(t *testing.T, table int) string {
	t.Helper()
	out, err := exec.Command("ip", "-4", "route", "show", "table", strconv.Itoa(table)).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "does not exist") {
			return ""
		}
		require.NoErrorf(t, err, "ip -4 route show table %d: %s", table, out)
	}
	return string(out)
}

// route6TableDump is the IPv6 counterpart of routeTableDump.
func route6TableDump(t *testing.T, table int) string {
	t.Helper()
	out, err := exec.Command("ip", "-6", "route", "show", "table", strconv.Itoa(table)).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "does not exist") {
			return ""
		}
		require.NoErrorf(t, err, "ip -6 route show table %d: %s", table, out)
	}
	return string(out)
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
