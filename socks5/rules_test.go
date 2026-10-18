package socks5

import (
	"net"
	"net/netip"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

func TestIsAllowedExitAddr(t *testing.T) {
	awlPrefixes := []netip.Prefix{netip.MustParsePrefix("10.66.0.0/16"), netip.MustParsePrefix("fd66::/64")}
	cases := []struct {
		addr  string
		allow bool
	}{
		{"8.8.8.8", true},
		{"2a00:1450:4001::1", true},
		{"::ffff:8.8.8.8", true},

		{"127.0.0.1", false},
		{"127.1.2.3", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"::ffff:0.0.0.0", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"::ffff:192.168.1.1", false},
		{"100.64.0.1", false},
		{"fd00::1", false},
		{"224.0.0.251", false},
		{"ff02::fb", false},
		{"10.66.0.2", false},
		{"fd66::2", false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.allow, isAllowedExitAddr(netip.MustParseAddr(tc.addr), awlPrefixes), tc.addr)
	}

	// An awl subnet outside the private ranges is refused too.
	require.False(t, isAllowedExitAddr(netip.MustParseAddr("203.0.113.5"), []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}))
	require.True(t, isAllowedExitAddr(netip.MustParseAddr("203.0.113.5"), nil))
}

func TestIsAwlName(t *testing.T) {
	for _, name := range []string{"awl", "peer.awl", "PEER.AWL", "peer.awl.", "admin.awl", "laptop.home.awl"} {
		require.True(t, isAwlName(name), name)
	}
	for _, name := range []string{"example.com", "awl.example.com", "notawl", "peer.awl.com"} {
		require.False(t, isAwlName(name), name)
	}
}

// TestExitNodeRefusesLocalHost is the S1 regression test: no form of a local
// address reaches a service listening on the exit node's loopback.
func TestExitNodeRefusesLocalHost(t *testing.T) {
	server := NewServer(nil, nil)
	proxyAddr := startTestProxy(t, server)

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

func TestExitNodeRefusesAwlNames(t *testing.T) {
	server := NewServer(nil, nil)
	// Even with the rules off: the name never reaches the system resolver.
	server.SetRulesEnabled(false)
	proxyAddr := startTestProxy(t, server)

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	require.NoError(t, err)
	conn, err := dialer.Dial("tcp", "peer.awl:80")
	if err == nil {
		conn.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "host unreachable")
}

// startTestProxy serves every connection to the returned address with server.
func startTestProxy(t *testing.T, server *Server) string {
	t.Helper()
	listenAddr := pickFreeAddr(t)
	client, err := NewClient(listenAddr, "", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	go func() {
		for conn := range client.ConnsChan() {
			go func() { _ = server.ServeConn(conn) }()
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
