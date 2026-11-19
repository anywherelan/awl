//go:build linux && vpn_hostnet

// Package netstate host-network integration tests.
//
// These tests exercise the real Linux netfilter / netlink plumbing behind
// Manager (EnableServerNAT/DisableServerNAT and
// EnableClientRoutes/DisableClientRoutes) against the *actual* host network:
// they create a dummy `awl0` link, install ip rules, iptables chains and
// routes, and assert they are applied and then fully torn down.
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
//	go test -c -tags vpn_hostnet -o gw-hostnet.test ./vpn/netstate/
//	sudo ./gw-hostnet.test -test.run '^TestGatewayHostNet' -test.v
//
// A dummy link (not a real TUN) is enough: the production code only references
// the interface by name (`-i/-o awl0`, route oif). The one behavioural
// difference — a real userspace TUN's routes self-destruct when the process
// dies, a dummy's do not — is never relied upon here: we always either tear
// down explicitly or delete the link to simulate that self-destruction.
package netstate

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/libp2p/go-reuseport"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"go.uber.org/goleak"

	"github.com/anywherelan/awl/vpn/privatenet"
)

const (
	testTunIf      = "awl0"
	testAwlSubnet  = "10.66.0.0/16"
	testAwlSubnet6 = "fd00:66::/48"
	ipForwardPath  = "/proc/sys/net/ipv4/ip_forward"
)

// Our address with the awl subnet length, as EnableServerNAT takes it.
var (
	testAwlPrefix  = netip.MustParsePrefix("10.66.0.1/16")
	testAwlPrefix6 = netip.MustParsePrefix("fd00:66::1/48")
)

// ---- N1: NAT apply/teardown lifecycle ----

func TestGatewayHostNetNATLifecycle(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	setupDummyTun(t)
	captureForward(t)
	// Start with forwarding off, as on a host where awl starts before Docker.
	require.NoError(t, os.WriteFile(ipForwardPath, []byte("0"), 0o600))

	before := snapshotNet(t)

	mgr := NewManager()
	require.NoError(t, mgr.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf))
	require.True(t, mgr.ServerNATActive())

	assertNATApplied(t)
	require.Equal(t, "1", readForward(t), "ip_forward must be on while NAT is up")

	require.NoError(t, mgr.DisableServerNAT())
	require.False(t, mgr.ServerNATActive())

	require.Equal(t, before, snapshotNet(t), "teardown must restore the exact pre-setup netfilter state")
	require.Equal(t, "1", readForward(t),
		"teardown must leave ip_forward on: other software may rely on it by then")
}

// ---- N2: NAT re-setup is idempotent (kill -9 recovery) ----
//
// A second manager enabling NAT over the live state of a first one is exactly
// what happens after a crash (the first manager died with its process without
// a teardown): cleanupStaleNAT must recover the leftover scaffolding so
// NewChain succeeds and the resulting state is identical to a single clean
// setup. A second Enable on the SAME manager would be short-circuited by the
// Manager-level idempotency, so a fresh instance is required here.

func TestGatewayHostNetNATIdempotentResetup(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	setupDummyTun(t)
	captureForward(t)

	before := snapshotNet(t)

	mgr1 := NewManager()
	require.NoError(t, mgr1.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf))
	applied1 := snapshotNet(t)

	// awl before v0.21.0 added MASQUERADE without the conntrack condition.
	// cleanupStaleNAT must remove such a leftover too.
	mustCmd(t, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", testAwlSubnet, "!", "-o", testTunIf, "-j", "MASQUERADE")

	// Second manager over the live state of the first — simulates a leftover
	// from a process that was killed before teardown ran.
	mgr2 := NewManager()
	require.NoError(t, mgr2.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf),
		"re-setup over leftover state must succeed (cleanupStaleNAT)")
	applied2 := snapshotNet(t)

	require.Equal(t, applied1, applied2, "re-setup must produce identical state, no duplicate rules")

	require.NoError(t, mgr2.DisableServerNAT())
	require.Equal(t, before, snapshotNet(t), "single teardown must clean everything after a re-setup")
}

// ---- N3: NAT leaves a pre-existing ip_forward=1 untouched ----

func TestGatewayHostNetNATPreservesExistingIPForward(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	setupDummyTun(t)
	captureForward(t)

	if readForward(t) != "1" {
		t.Skip("ip_forward is not already on; this case needs a host where it was pre-enabled (e.g. Docker)")
	}

	mgr := NewManager()
	require.NoError(t, mgr.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf))
	require.Equal(t, "1", readForward(t))

	require.NoError(t, mgr.DisableServerNAT())
	require.Equal(t, "1", readForward(t),
		"ip_forward was already on before setup; teardown must NOT reset it to 0")
}

// ---- N4: a port the host publishes on our awl address ----
//
// Docker publishes a port through DNAT: a connection to our awl address goes on
// to a container, through FORWARD. AWL-FORWARD must return it to the host's
// rules instead of dropping it as a private destination, also when our jumps
// are on top of FORWARD (awl started after Docker). The container must see the
// peer's address, not ours. A connection straight to the container address is
// gateway transit to a private network and must still be dropped.
//
// The peer and the container are network namespaces held by processes, so the
// test runs both under sudo and inside `unshare -rn`.

const testBridgeIf = "awltestbr0"

func TestGatewayHostNetNATLocalDNAT(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	captureForward(t)

	peer, ctr := startPeerAndLANNetns(t)
	for _, step := range []struct{ netns, cmd string }{
		// The container answers port 80 only to the peer's address: with our
		// MASQUERADE it would see the address of the host on its network.
		{ctr, "iptables -A INPUT -p tcp --dport 80 ! -s 10.66.0.2 -j DROP"},
		{ctr, "ip6tables -A INPUT -p tcp --dport 80 ! -s fd00:66::2 -j DROP"},
	} {
		nsCmd(t, step.netns, step.cmd)
	}

	// The host's rules, like Docker's: DNAT of the published port 3080 and
	// ACCEPT for the container network. They go in before EnableServerNAT, so
	// our jumps end up above them.
	for _, rule := range []string{
		"iptables -t nat -I PREROUTING -d 10.66.0.1 -p tcp --dport 3080 -j DNAT --to-destination 172.31.254.2:80",
		"ip6tables -t nat -I PREROUTING -d fd00:66::1 -p tcp --dport 3080 -j DNAT --to-destination [fd31:254::2]:80",
		"iptables -I FORWARD -o " + testBridgeIf + " -j ACCEPT",
		"iptables -I FORWARD -i " + testBridgeIf + " -j ACCEPT",
		"ip6tables -I FORWARD -o " + testBridgeIf + " -j ACCEPT",
		"ip6tables -I FORWARD -i " + testBridgeIf + " -j ACCEPT",
	} {
		args := strings.Fields(rule)
		mustCmd(t, args[0], args[1:]...)
		del := strings.Fields(strings.Replace(rule, " -I ", " -D ", 1))
		t.Cleanup(func() { _ = exec.Command(del[0], del[1:]...).Run() })
	}

	before := snapshotNet(t)

	mgr := NewManager()
	require.NoError(t, mgr.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf))

	// Straight to the container we use port 81: the container's own DROP
	// covers only port 80, so a packet that passes our rules gets an answer.
	for _, c := range []struct {
		name, addr, port string
		reaches          bool
	}{
		{"IPv4, published port on our address", "10.66.0.1", "3080", true},
		{"IPv6, published port on our address", "fd00:66::1", "3080", true},
		{"IPv4, straight to the container", "172.31.254.2", "81", false},
		{"IPv6, straight to the container", "fd31:254::2", "81", false},
	} {
		require.Equal(t, c.reaches, tcpReaches(t, peer, c.addr, c.port), c.name)
	}

	require.NoError(t, mgr.DisableServerNAT())
	require.Equal(t, before, snapshotNet(t), "teardown must restore the exact pre-setup netfilter state")
}

// ---- N5: a peer with LAN access ----
//
// A peer with LAN access reaches a host in the LAN of the exit node, a peer
// without it does not, and the list of these peers is applied at runtime and
// kept for the next enable. The LAN host is a network namespace, as the
// container in N4. Nothing listens in it: "connection refused" means the
// packet got there.

func TestGatewayHostNetNATLANAccess(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	captureForward(t)

	peer, _ := startPeerAndLANNetns(t)
	// The host lets the LAN traffic through FORWARD, as a router does. Our
	// jumps end up above these rules.
	for _, rule := range []string{
		"iptables -I FORWARD -o " + testBridgeIf + " -j ACCEPT",
		"iptables -I FORWARD -i " + testBridgeIf + " -j ACCEPT",
		"ip6tables -I FORWARD -o " + testBridgeIf + " -j ACCEPT",
		"ip6tables -I FORWARD -i " + testBridgeIf + " -j ACCEPT",
	} {
		args := strings.Fields(rule)
		mustCmd(t, args[0], args[1:]...)
		del := strings.Fields(strings.Replace(rule, " -I ", " -D ", 1))
		t.Cleanup(func() { _ = exec.Command(del[0], del[1:]...).Run() })
	}
	requireLANReached := func(want bool, msg string) {
		t.Helper()
		require.Equal(t, want, tcpReaches(t, peer, "172.31.254.2", "81"), "IPv4: "+msg)
		require.Equal(t, want, tcpReaches(t, peer, "fd31:254::2", "81"), "IPv6: "+msg)
	}
	// fromAddrs makes the peer send from src4 and src6.
	fromAddrs := func(src4, src6 string) {
		t.Helper()
		nsCmd(t, peer, "ip route replace default via 10.66.0.1 src "+src4)
		nsCmd(t, peer, "ip -6 route replace default via fd00:66::1 src "+src6)
	}
	// A second address of the peer, standing for a peer without LAN access.
	// The kernel would pick the new IPv6 address as the source.
	nsCmd(t, peer, "ip addr add 10.66.0.4/16 dev eth0")
	nsCmd(t, peer, "ip -6 addr add fd00:66::4/48 dev eth0")
	fromAddrs("10.66.0.2", "fd00:66::2")
	lanPeers := []netip.Addr{netip.MustParseAddr("fd00:66::2"), netip.MustParseAddr("10.66.0.2")}

	before := snapshotNet(t)

	mgr := NewManager()
	require.NoError(t, mgr.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf))
	assertNATApplied(t)
	requireLANReached(false, "no peer has LAN access yet")

	require.NoError(t, mgr.SetServerLANAccess(lanPeers))
	assertLANChains(t, lanPeers)
	requireLANReached(true, "the peer has LAN access at runtime")

	fromAddrs("10.66.0.4", "fd00:66::4")
	requireLANReached(false, "another peer has no LAN access")
	fromAddrs("10.66.0.2", "fd00:66::2")

	// A second manager over the live state of the first, as after a crash
	// (see N2): cleanupStaleNAT must remove the filled AWL-LAN chains too.
	// The list is set before the enable: it is kept while NAT is off.
	applied := snapshotNet(t)
	mgr2 := NewManager()
	require.NoError(t, mgr2.SetServerLANAccess(lanPeers))
	require.NoError(t, mgr2.EnableServerNAT(testAwlPrefix, testAwlPrefix6, testTunIf),
		"re-setup over leftover state must succeed (cleanupStaleNAT)")
	require.Equal(t, applied, snapshotNet(t), "re-setup must produce identical state, no duplicate rules")
	requireLANReached(true, "the peer has LAN access after a re-setup")

	require.NoError(t, mgr2.SetServerLANAccess(nil))
	assertLANChains(t, nil)
	requireLANReached(false, "the peer's LAN access is revoked")

	require.NoError(t, mgr2.DisableServerNAT())
	require.Equal(t, before, snapshotNet(t), "single teardown must clean everything after a re-setup")
}

// startPeerAndLANNetns starts two network namespaces: the peer, connected to
// the host over testTunIf, which stands for the TUN, and a LAN host, connected
// over testBridgeIf. Both reach the host's other networks through it. Returns
// their PIDs.
//
//	peer      10.66.0.2/16    fd00:66::2/48  — testTunIf:    10.66.0.1/16    fd00:66::1/48
//	LAN host  172.31.254.2/24 fd31:254::2/64 — testBridgeIf: 172.31.254.1/24 fd31:254::1/64
func startPeerAndLANNetns(t *testing.T) (peer, lan string) {
	t.Helper()
	peer, lan = startNetns(t), startNetns(t)
	_ = exec.Command("ip", "link", "del", testTunIf).Run() // best-effort pre-clean
	mustCmd(t, "ip", "link", "add", testTunIf, "type", "veth", "peer", "name", "eth0", "netns", peer)
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", testTunIf).Run() })
	mustCmd(t, "ip", "link", "add", testBridgeIf, "type", "veth", "peer", "name", "eth0", "netns", lan)
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", testBridgeIf).Run() })

	// No DAD: IPv6 on a fresh link is otherwise not usable for about a second.
	for _, step := range []struct{ netns, cmd string }{
		{"", "sysctl -qw net.ipv6.conf." + testTunIf + ".accept_dad=0"},
		{"", "ip addr add " + testAwlPrefix.String() + " dev " + testTunIf},
		{"", "ip addr add " + testAwlPrefix6.String() + " dev " + testTunIf},
		{"", "ip link set " + testTunIf + " up"},
		{"", "sysctl -qw net.ipv6.conf." + testBridgeIf + ".accept_dad=0"},
		{"", "ip addr add 172.31.254.1/24 dev " + testBridgeIf},
		{"", "ip addr add fd31:254::1/64 dev " + testBridgeIf},
		{"", "ip link set " + testBridgeIf + " up"},
		{peer, "sysctl -qw net.ipv6.conf.eth0.accept_dad=0"},
		{peer, "ip addr add 10.66.0.2/16 dev eth0"},
		{peer, "ip addr add fd00:66::2/48 dev eth0"},
		{peer, "ip link set eth0 up"},
		{peer, "ip route add default via 10.66.0.1"},
		{peer, "ip -6 route add default via fd00:66::1"},
		{lan, "sysctl -qw net.ipv6.conf.eth0.accept_dad=0"},
		{lan, "ip addr add 172.31.254.2/24 dev eth0"},
		{lan, "ip addr add fd31:254::2/64 dev eth0"},
		{lan, "ip link set eth0 up"},
		{lan, "ip route add default via 172.31.254.1"},
		{lan, "ip -6 route add default via fd31:254::1"},
	} {
		nsCmd(t, step.netns, step.cmd)
	}
	return peer, lan
}

// nsCmd runs cmd in the network namespace of pid, or in ours if pid is empty.
func nsCmd(t *testing.T, pid, cmd string) {
	t.Helper()
	args := strings.Fields(cmd)
	if pid != "" {
		args = append([]string{"nsenter", "-t", pid, "-n"}, args...)
	}
	mustCmd(t, args[0], args[1:]...)
}

// ---- R1: route apply/teardown lifecycle ----

// The enable→disable sequence runs TWICE with the same manager: a runtime
// re-enable after a disable is a real user flow (and the mirror of the Windows
// ClientRoutesLifecycle test), so pin that each enable applies the full state
// and each disable restores the exact pre-setup snapshot.
func TestGatewayHostNetRoutesLifecycle(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	requireDefaultRoute(t)
	setupDummyTun(t)

	before := snapshotNet(t)

	mgr := NewManager()
	for cycle := 1; cycle <= 2; cycle++ {
		require.NoError(t, mgr.EnableClientRoutes(testTunIf), "cycle %d", cycle)
		require.True(t, mgr.ClientRoutesActive(), "cycle %d", cycle)

		assertRoutesApplied(t)

		require.NoError(t, mgr.DisableClientRoutes(), "cycle %d", cycle)
		require.False(t, mgr.ClientRoutesActive(), "cycle %d", cycle)
		require.Equal(t, before, snapshotNet(t),
			"cycle %d: teardown must restore the exact pre-setup routing state", cycle)
	}
}

// ---- R2: route stale-recovery after the TUN's own default self-destructed ----
//
// Deleting the link drops the kernel default route via it (mirrors a real TUN
// dying with its process), while the fwmark rule and the auxiliary-table copies
// are orphaned. A fresh manager's EnableClientRoutes must clean those up
// (cleanupStaleRoutes) and succeed without an EEXIST on the new TUN default.

func TestGatewayHostNetRoutesStaleRecovery(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	requireDefaultRoute(t)
	setupDummyTun(t)

	before := snapshotNet(t)

	// No Start → no route monitor: mgr1 is abandoned with its OS state orphaned
	// on purpose for cleanupStaleRoutes to recover, exactly like a process that
	// died before teardown.
	mgr1 := NewManager()
	require.NoError(t, mgr1.EnableClientRoutes(testTunIf))
	applied1 := snapshotNet(t)

	// Simulate the TUN dying: deleting awl0 makes the kernel auto-remove the
	// default route via it, leaving the fwmark rule + table copies orphaned.
	recreateDummyTun(t)

	mgr2 := NewManager()
	require.NoError(t, mgr2.EnableClientRoutes(testTunIf),
		"re-setup must recover orphaned ip rule + table routes (cleanupStaleRoutes)")
	require.Equal(t, applied1, snapshotNet(t), "recovered state must match a clean single setup")

	require.NoError(t, mgr2.DisableClientRoutes())
	require.Equal(t, before, snapshotNet(t))
}

// ---- R3: a leftover TUN default route is reported, not silently clobbered ----
//
// cleanupStaleRoutes deliberately never deletes the TUN default route (no
// reliable owner-tag; deleting by metric risks nuking dhclient/OpenVPN routes).
// So when the link did NOT go away (here: a still-present dummy with the route
// seeded), EnableClientRoutes must surface a clear diagnostic instead of
// duplicating or clobbering.

func TestGatewayHostNetRoutesLeftoverTunRouteErrors(t *testing.T) {
	verifyNoLeaks(t)
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

	mgr := NewManager()
	err = mgr.EnableClientRoutes(testTunIf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "leftover from a prior awl run",
		"a colliding TUN default must produce the operator-facing diagnostic")
	require.False(t, mgr.ClientRoutesActive(), "a failed enable must leave the manager inactive")

	require.NoError(t, netlink.RouteDel(leftover))
	require.Equal(t, before, snapshotNet(t), "the failed setup must leave no partial state behind")
}

// ---- R4: route-change monitor keeps the exemption table in sync ----
//
// After setup, the monitor (netlink RTM_NEWROUTE/DELROUTE subscription) must
// mirror host default-route changes into tableID so marked libp2p sockets keep
// a physical-NIC exit across a DHCP renew / uplink roam. Here we simulate a new
// uplink by adding a second default via a dummy interface into the main table,
// then removing it, asserting the awl table follows both edges. It must never
// disturb the TUN default route or the IPv6 fence.
func TestGatewayHostNetRoutesStalenessReconcile(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	requireDefaultRoute(t)
	setupDummyTun(t)

	// The monitor lives in Manager.Start (not in EnableClientRoutes), so start
	// it explicitly; cancel stops it before goleak's final check (VerifyNone's
	// built-in retries absorb the asynchronous goroutine exit).
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, mgr.Start(ctx))
	require.NoError(t, mgr.EnableClientRoutes(testTunIf))
	t.Cleanup(func() { _ = mgr.DisableClientRoutes() })

	// A second dummy "uplink" with an on-link subnet and its own default route,
	// at a high metric so it never competes with real egress or the TUN default.
	const dummy2 = "awldummy2"
	_ = exec.Command("ip", "link", "del", dummy2).Run() // best-effort pre-clean
	mustCmd(t, "ip", "link", "add", dummy2, "type", "dummy")
	mustCmd(t, "ip", "link", "set", dummy2, "up")
	mustCmd(t, "ip", "addr", "add", "192.0.2.1/24", "dev", dummy2)
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", dummy2).Run() })

	mustCmd(t, "ip", "route", "add", "default", "via", "192.0.2.2", "dev", dummy2, "metric", "4000")

	require.Eventually(t, func() bool {
		return strings.Contains(routeTableDump(t, tableID), "dev "+dummy2)
	}, 5*time.Second, 100*time.Millisecond,
		"the monitor must copy the new host default into the awl exemption table")

	// The TUN default and IPv6 fence must be untouched by the reconcile.
	assertRoutesApplied(t)

	// Remove the second default; the awl table copy must follow.
	mustCmd(t, "ip", "route", "del", "default", "via", "192.0.2.2", "dev", dummy2, "metric", "4000")

	require.Eventually(t, func() bool {
		return !strings.Contains(routeTableDump(t, tableID), "dev "+dummy2)
	}, 5*time.Second, 100*time.Millisecond,
		"the monitor must remove the stale default from the awl exemption table")

	// The remove edge, like the add edge, must leave the TUN default and IPv6
	// fence intact — reconcile only ever adjusts the exemption copies.
	assertRoutesApplied(t)
}

// ---- R5: IPv6 route-change monitor keeps the exemption table in sync ----
//
// The IPv6 counterpart of R4. RA re-advertising a new router/prefix changes the
// v6 default far more often than IPv4 changes, so the monitor must mirror v6
// default changes into tableID too (marked libp2p v6 sockets otherwise fall
// through to the `unreachable ::/0` fence and get EHOSTUNREACH). We simulate a
// new v6 uplink with a high-metric default via a dummy interface and assert the
// awl table follows both edges without disturbing the fence or the TUN default.
func TestGatewayHostNetRoutesStalenessReconcileV6(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	requireDefaultRoute(t)
	// The v6 fence/exemption path (and thus v6 reconcile) only exists when the
	// IPv6 stack is present; a kernel-level disable (ipv6.disable=1) removes it.
	if _, err := os.Stat("/proc/sys/net/ipv6"); os.IsNotExist(err) {
		t.Skip("no IPv6 stack on this host; the v6 exemption path is not installed")
	}
	setupDummyTun(t)

	// See R4 for why Start + cancel are needed here.
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, mgr.Start(ctx))
	require.NoError(t, mgr.EnableClientRoutes(testTunIf))
	t.Cleanup(func() { _ = mgr.DisableClientRoutes() })

	// A second dummy "uplink" with an on-link v6 subnet and its own v6 default,
	// at a high metric so it never competes with real egress or the fence.
	const dummy2 = "awldummy2"
	_ = exec.Command("ip", "link", "del", dummy2).Run() // best-effort pre-clean
	mustCmd(t, "ip", "link", "add", dummy2, "type", "dummy")
	mustCmd(t, "ip", "link", "set", dummy2, "up")
	mustCmd(t, "ip", "addr", "add", "2001:db8::1/64", "dev", dummy2, "nodad")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", dummy2).Run() })

	mustCmd(t, "ip", "-6", "route", "add", "default", "via", "2001:db8::2", "dev", dummy2, "metric", "4000")

	require.Eventually(t, func() bool {
		return strings.Contains(route6TableDump(t, tableID), "dev "+dummy2)
	}, 5*time.Second, 100*time.Millisecond,
		"the monitor must copy the new host IPv6 default into the awl exemption table")

	// The unreachable fence and TUN default must be untouched by the reconcile.
	assertRoutesApplied(t)

	// Remove the second v6 default; the awl table copy must follow.
	mustCmd(t, "ip", "-6", "route", "del", "default", "via", "2001:db8::2", "dev", dummy2, "metric", "4000")

	require.Eventually(t, func() bool {
		return !strings.Contains(route6TableDump(t, tableID), "dev "+dummy2)
	}, 5*time.Second, 100*time.Millisecond,
		"the monitor must remove the stale IPv6 default from the awl exemption table")

	assertRoutesApplied(t)
}

// ---- R6: offline enable proceeds and self-heals when a default appears ----
//
// Enabling the gateway client with NO IPv4 default route must succeed (warn +
// empty exemption table) with the TUN default and the v6 fence installed, and
// the monitor must copy the host default into tableID once one appears — the
// §8.4 offline-boot self-heal. We simulate "offline" by removing the host's
// real IPv4 defaults and restoring them afterwards; while they are gone the
// host's v4 egress is the TUN black hole, which is within this suite's risk
// profile (every routes test black-holes egress mid-flight anyway).
func TestGatewayHostNetRoutesOfflineEnableSelfHeals(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)
	// A default is required not by the enable under test (that is the point)
	// but as material: we snapshot the real defaults to remove and re-add.
	requireDefaultRoute(t)
	setupDummyTun(t)

	origDefaults, err := getDefaultRoutes()
	require.NoError(t, err)
	require.NotEmpty(t, origDefaults)
	for i := range origDefaults {
		r := origDefaults[i]
		require.NoError(t, netlink.RouteDel(&r), "remove host default %v", r)
	}
	t.Cleanup(func() {
		// RouteReplace, not RouteAdd: the mid-test re-add below leaves one of
		// them already present on the success path.
		for i := range origDefaults {
			r := origDefaults[i]
			_ = netlink.RouteReplace(&r)
		}
	})

	before := snapshotNet(t)

	// See R4 for why Start + cancel are needed here.
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, mgr.Start(ctx))
	require.NoError(t, mgr.EnableClientRoutes(testTunIf),
		"offline enable must proceed with a warning, not fail")
	t.Cleanup(func() { _ = mgr.DisableClientRoutes() })
	require.True(t, mgr.ClientRoutesActive())

	// Offline shape: TUN default captured, exemption table empty (nothing to
	// exempt yet). assertRoutesApplied is not usable here — it requires a
	// non-empty exemption table.
	main := cmdOut(t, "ip", "-4", "route", "show")
	require.Contains(t, main, "dev "+testTunIf, "TUN default must be installed even offline")
	require.Empty(t, strings.TrimSpace(routeTableDump(t, tableID)),
		"the v4 exemption table must start empty when enabled offline")

	// The network comes back: restore one real default in the main table.
	restored := origDefaults[0]
	require.NoError(t, netlink.RouteReplace(&restored))

	require.Eventually(t, func() bool {
		return strings.Contains(routeTableDump(t, tableID), "default")
	}, 5*time.Second, 100*time.Millisecond,
		"the monitor must copy the appeared host default into the awl exemption table")

	// The reconcile must have left the TUN default and the IPv6 fence alone.
	assertRoutesApplied(t)

	require.NoError(t, mgr.DisableClientRoutes())
	require.False(t, mgr.ClientRoutesActive())

	// Back out the mid-test re-add so the state is comparable with the
	// "no defaults" snapshot; teardown must have removed everything else.
	require.NoError(t, netlink.RouteDel(&restored))
	require.Equal(t, before, snapshotNet(t), "teardown must restore the exact pre-setup routing state")
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
		"-A " + awlForwardChain + " -m conntrack --ctorigdst " + testAwlPrefix.Addr().String() + " -j RETURN",
		"-A " + awlForwardChain + " -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu",
		"-A " + awlForwardChain + " -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"-A " + awlForwardChain + " -j " + awlLANChain,
	}
	for _, p := range privatenet.PrefixesV4() {
		want = append(want, "-A "+awlForwardChain+" -d "+p.String()+" -j DROP")
	}
	want = append(want, "-A "+awlForwardChain+" -j ACCEPT")

	got := lines(cmdOut(t, "iptables", "-S", awlForwardChain))
	require.Equal(t, want, got, "AWL-FORWARD chain content/order")
	assertLANChains(t, nil)

	// iptables -S prints matches in its own canonical order (-s before -i,
	// -d before -o), regardless of the order the code passes them in
	// (outboundJumpArgs/returnJumpArgs use -i/-s and -o/-d), so assert against
	// that canonical form.
	filter := cmdOut(t, "iptables", "-S", "FORWARD")
	require.Contains(t, filter, "-s "+testAwlSubnet+" -i "+testTunIf+" -j "+awlForwardChain, "outbound jump")
	require.Contains(t, filter, "-d "+testAwlSubnet+" -o "+testTunIf+" -j "+awlForwardChain, "return jump")

	nat := cmdOut(t, "iptables", "-t", "nat", "-S", "POSTROUTING")
	require.Contains(t, nat, "-s "+testAwlSubnet+" ! -o "+testTunIf+" -m conntrack ! --ctorigdst "+testAwlPrefix.Addr().String()+" -j MASQUERADE", "MASQUERADE")
}

// assertLANChains checks the AWL-LAN and AWL6-LAN chains hold the rules for
// lanPeers, in order.
func assertLANChains(t *testing.T, lanPeers []netip.Addr) {
	t.Helper()

	for _, c := range []struct {
		cmd, chain, awlSubnet string
		ipv6                  bool
		lanPrefixes           []netip.Prefix
	}{
		{"iptables", awlLANChain, testAwlSubnet, false, privatenet.LANPrefixesV4()},
		{"ip6tables", awlLANChain6, testAwlSubnet6, true, privatenet.LANPrefixesV6()},
	} {
		want := []string{"-N " + c.chain}
		if peers := lanPeersOf(normalizeLANPeers(lanPeers), c.ipv6); len(peers) > 0 {
			want = append(want, "-A "+c.chain+" -d "+c.awlSubnet+" -j RETURN")
			for _, peer := range peers {
				for _, lan := range c.lanPrefixes {
					want = append(want, "-A "+c.chain+" -s "+netip.PrefixFrom(peer, peer.BitLen()).String()+" -d "+lan.String()+" -j ACCEPT")
				}
			}
		}
		require.Equal(t, want, lines(cmdOut(t, c.cmd, "-S", c.chain)), "%s chain content/order", c.chain)
	}
}

func assertRoutesApplied(t *testing.T) {
	t.Helper()

	rules := cmdOut(t, "ip", "rule", "show")
	require.Contains(t, rules, fmt.Sprintf("fwmark 0x%x", awlMark), "fwmark ip rule")
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
	require.Contains(t, rules6, fmt.Sprintf("fwmark 0x%x", awlMark), "v6 fwmark ip rule")
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

// snapshotNet captures every piece of host network state setupNAT/setupGatewayRoutes
// can touch, EXCEPT ip_forward (which stays on after teardown and is asserted
// separately). Lines within each section are sorted so the comparison is
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

// ---- Marked TCP dial from the listen port ----

// TestGatewayHostNetMarkedDialReusesListenPort checks the socket setup that
// p2p.reuseportDialer performs, with the real SO_MARK instead of the no-op
// control func its own tests use: a dial bound to the port of a live listener,
// with SO_REUSEPORT and the mark both applied before bind. Outbound TCP has to
// leave from the listen port for TCP hole punching to work at all — see
// plans/tcp-hole-punching.md.
func TestGatewayHostNetMarkedDialReusesListenPort(t *testing.T) {
	verifyNoLeaks(t)
	requireRoot(t)

	listener, err := reuseport.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	mark := NewManager().ControlFunc()
	dialer := net.Dialer{
		LocalAddr: listener.Addr(),
		Timeout:   5 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			if err := reuseport.Control(network, address, c); err != nil {
				return err
			}
			return mark(network, address, c)
		},
	}
	conn, err := dialer.Dial("tcp4", target.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.Equal(t, listener.Addr().(*net.TCPAddr).Port, conn.LocalAddr().(*net.TCPAddr).Port)

	raw, err := conn.(*net.TCPConn).SyscallConn()
	require.NoError(t, err)
	var gotMark int
	var sockErr error
	require.NoError(t, raw.Control(func(fd uintptr) {
		gotMark, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK)
	}))
	require.NoError(t, sockErr)
	require.Equal(t, awlMark, gotMark)

	// The listener keeps accepting while a dialed connection shares its port.
	inbound, err := net.DialTimeout("tcp4", listener.Addr().String(), 5*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inbound.Close() })
	require.NoError(t, listener.(*net.TCPListener).SetDeadline(time.Now().Add(5*time.Second)))
	accepted, err := listener.Accept()
	require.NoError(t, err)
	_ = accepted.Close()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// verifyNoLeaks asserts the test spawns no goroutine that outlives it — chiefly
// the route-change monitor and its netlink watcher goroutines, which die when
// the ctx passed to Manager.Start is cancelled. Registered via t.Cleanup (not
// defer) and as the FIRST thing each test does, so — t.Cleanup being LIFO — it
// runs LAST, after every teardown/cancel/link-delete cleanup, once nothing
// legitimate is left running (VerifyNone's built-in retries absorb the
// asynchronous exit after cancel). IgnoreCurrent snapshots pre-existing
// background goroutines (logging etc.) so only goroutines started by the test
// itself are held against it.
func verifyNoLeaks(t *testing.T) {
	t.Helper()
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("requires root (CAP_NET_ADMIN); build with -tags vpn_hostnet and run the binary via sudo")
	}
}

// requireDefaultRoute skips if the host has no IPv4 default route, since
// setupGatewayRoutes legitimately refuses to run without one.
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

// startNetns starts a process in a new network namespace and returns its PID
// for `ip link ... netns` and `nsenter -t`. The namespace, and the links in it,
// go away with the process at cleanup.
func startNetns(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("unshare", "-n", "sh", "-c", "echo ready; exec sleep 60")
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	// The line comes after unshare, so the PID is already in the new namespace.
	line, err := bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	return strconv.Itoa(cmd.Process.Pid)
}

// tcpReaches opens a TCP connection from the network namespace of pid. Nothing
// listens on the far side: "connection refused" means the SYN got there and
// the RST came back, a timeout means a packet was dropped on the way.
func tcpReaches(t *testing.T, pid, addr, port string) bool {
	t.Helper()
	cmd := exec.Command("nsenter", "-t", pid, "-n", "timeout", "1", "bash", "-c", "exec 3<>/dev/tcp/"+addr+"/"+port)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "Connection refused") {
		return true
	}
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 124 { // timeout
		return false
	}
	t.Fatalf("connect to %s port %s: %v: %s", addr, port, err, out)
	return false
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
// test, so tests stay order-independent although teardown leaves it on.
func captureForward(t *testing.T) {
	t.Helper()
	orig := readForward(t)
	t.Cleanup(func() { _ = os.WriteFile(ipForwardPath, []byte(orig), 0o600) })
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
