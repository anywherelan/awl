package socks5

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"

	"github.com/anywherelan/awl/vpn/privatenet"
)

func TestExitAddrRefusal(t *testing.T) {
	awlPrefixes := []netip.Prefix{netip.MustParsePrefix("10.66.0.0/16"), netip.MustParsePrefix("fd66::/64")}
	cases := []struct {
		addr string
		// empty when the address is allowed
		refusal string
	}{
		{"8.8.8.8", ""},
		{"2a00:1450:4001::1", ""},
		{"::ffff:8.8.8.8", ""},

		{"127.0.0.1", "loopback_address"},
		{"127.1.2.3", "loopback_address"},
		{"::1", "loopback_address"},
		{"::ffff:127.0.0.1", "loopback_address"},
		{"0.0.0.0", "loopback_address"},
		{"::", "loopback_address"},
		{"::ffff:0.0.0.0", "loopback_address"},
		{"169.254.169.254", "link_local_address"},
		{"fe80::1", "link_local_address"},
		{"10.1.2.3", "private_address"},
		{"172.16.0.1", "private_address"},
		{"192.168.1.1", "private_address"},
		{"::ffff:192.168.1.1", "private_address"},
		{"100.64.0.1", "private_address"},
		{"fd00::1", "private_address"},
		{"224.0.0.251", "multicast_address"},
		{"ff02::fb", "multicast_address"},
		{"10.66.0.2", "awl_address"},
		{"fd66::2", "awl_address"},
	}
	for _, tc := range cases {
		addr := netip.MustParseAddr(tc.addr)
		require.Equal(t, tc.refusal, exitAddrRefusal(addr, awlPrefixes, false), tc.addr)

		// A peer with LAN access reaches the LAN ranges, and nothing else
		// that is refused to the others.
		refusalLAN := tc.refusal
		if refusalLAN == "private_address" {
			refusalLAN = ""
		}
		require.Equal(t, refusalLAN, exitAddrRefusal(addr, awlPrefixes, true), "%s with LAN access", tc.addr)
	}

	// An awl subnet outside the private ranges is refused too.
	require.Equal(t, "awl_address", exitAddrRefusal(netip.MustParseAddr("203.0.113.5"), []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, false))
	require.Empty(t, exitAddrRefusal(netip.MustParseAddr("203.0.113.5"), nil, false))
}

// TestExitNodeRefusesLocalHost is the S1 regression test: no form of a local
// address reaches a service listening on the exit node's loopback, also for a
// peer with LAN access.
func TestExitNodeRefusesLocalHost(t *testing.T) {
	for _, allowLAN := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowLAN=%v", allowLAN), func(t *testing.T) {
			testExitNodeRefusesLocalHost(t, allowLAN)
		})
	}
}

func testExitNodeRefusesLocalHost(t *testing.T, allowLAN bool) {
	server := NewServer(nil, nil)
	proxyAddr := startTestProxy(t, server, allowLAN)

	port4 := listenLoopback(t, "tcp4", "127.0.0.1:0")
	targets := []string{
		net.JoinHostPort("127.0.0.1", port4),
		net.JoinHostPort("localhost", port4),
		net.JoinHostPort("::ffff:127.0.0.1", port4),
		net.JoinHostPort("0.0.0.0", port4),
	}
	if port6 := listenLoopback(t, "tcp6", "[::1]:0"); port6 != "" {
		targets = append(targets, net.JoinHostPort("::1", port6), net.JoinHostPort("::", port6))
	}

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	require.NoError(t, err)
	for _, target := range targets {
		conn, err := dialer.Dial("tcp", target)
		if err == nil {
			conn.Close()
		}
		require.Error(t, err, target)
		require.Contains(t, err.Error(), "not allowed by ruleset", target)
	}

	// The control: with the rules off, the same listener is reached.
	server.SetRulesEnabled(false)
	conn, err := dialer.Dial("tcp", targets[0])
	require.NoError(t, err)
	conn.Close()
}

// TestExitNodeLANAccess checks that a peer with LAN access reaches a LAN
// address of the exit node, and the others don't. It needs an interface with
// an address in a LAN range.
func TestExitNodeLANAccess(t *testing.T) {
	lanAddr := findLANAddr(t)
	port := listenLoopback(t, "tcp4", net.JoinHostPort(lanAddr.String(), "0"))
	target := net.JoinHostPort(lanAddr.String(), port)

	for _, allowLAN := range []bool{false, true} {
		dialer, err := proxy.SOCKS5("tcp", startTestProxy(t, NewServer(nil, nil), allowLAN), nil, proxy.Direct)
		require.NoError(t, err)
		conn, err := dialer.Dial("tcp", target)
		if allowLAN {
			require.NoError(t, err)
			conn.Close()
		} else {
			require.ErrorContains(t, err, "not allowed by ruleset")
		}
	}
}

// findLANAddr returns an IPv4 address of this host in one of the LAN ranges,
// or skips the test.
func findLANAddr(t *testing.T) netip.Addr {
	t.Helper()
	ifaceAddrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, ifaceAddr := range ifaceAddrs {
		ipNet, ok := ifaceAddr.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok || !addr.Unmap().Is4() {
			continue
		}
		addr = addr.Unmap()
		for _, lan := range privatenet.LANPrefixesV4() {
			if lan.Contains(addr) {
				return addr
			}
		}
	}
	t.Skip("no interface with an IPv4 address in a LAN range")
	return netip.Addr{}
}

func TestExitNodeRefusesAwlNames(t *testing.T) {
	server := NewServer(nil, nil)
	// Even with the rules off: the name never reaches the system resolver.
	server.SetRulesEnabled(false)
	proxyAddr := startTestProxy(t, server, false)

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	require.NoError(t, err)
	conn, err := dialer.Dial("tcp", "peer.awl:80")
	if err == nil {
		conn.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "host unreachable")
}

// startTestProxy serves every connection to the returned address with server,
// as for a peer with LAN access if allowLAN is set.
func startTestProxy(t *testing.T, server *Server, allowLAN bool) string {
	t.Helper()
	listenAddr := pickFreeAddr(t)
	client, err := NewClient(listenAddr, "", "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	go func() {
		for conn := range client.ConnsChan() {
			go func() { _ = server.ServeConn(conn, allowLAN) }()
		}
	}()

	return listenAddr
}

// listenLoopback accepts and drops connections on addr and returns its port,
// or "" if the address family is not available.
func listenLoopback(t *testing.T, network, addr string) string {
	t.Helper()
	l, err := net.Listen(network, addr)
	if err != nil {
		if network == "tcp6" {
			return ""
		}
		require.NoError(t, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}
