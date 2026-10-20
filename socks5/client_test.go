package socks5

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"testing"

	socks5Lib "github.com/haxii/socks5"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

func TestBypassTarget(t *testing.T) {
	awlPrefixes := []netip.Prefix{netip.MustParsePrefix("10.66.0.0/16"), netip.MustParsePrefix("fd00:66::/48")}
	bypassed := func(dest *socks5Lib.AddrSpec, want string) {
		t.Helper()
		got, ok := bypassTarget(dest, awlPrefixes)
		require.True(t, ok, dest.String())
		require.Equal(t, want, got, dest.String())
	}
	notBypassed := func(dest *socks5Lib.AddrSpec) {
		t.Helper()
		_, ok := bypassTarget(dest, awlPrefixes)
		require.False(t, ok, dest.String())
	}

	for name, want := range map[string]string{
		"laptop.awl":    "laptop.awl:80",
		"LAPTOP.awl.":   "LAPTOP.awl.:80",
		"unknown.awl":   "unknown.awl:80",
		"admin.awl":     "admin.awl:80",
		"localhost":     "localhost:80",
		"app.localhost": "localhost:80",
	} {
		bypassed(&socks5Lib.AddrSpec{FQDN: name, Port: 80}, want)
	}

	// An IP address comes as an address or, from Chrome, as a name.
	for ip, want := range map[string]string{
		"127.0.0.1":        "127.0.0.1:80",
		"127.1.2.3":        "127.1.2.3:80",
		"::1":              "[::1]:80",
		"::ffff:127.0.0.1": "127.0.0.1:80",
		"10.66.0.2":        "10.66.0.2:80",
		"10.66.1.1":        "10.66.1.1:80",
		"fd00:66::2":       "[fd00:66::2]:80",
	} {
		bypassed(&socks5Lib.AddrSpec{IP: net.ParseIP(ip), Port: 80}, want)
		bypassed(&socks5Lib.AddrSpec{FQDN: ip, Port: 80}, want)
	}
	for _, ip := range []string{"8.8.8.8", "0.0.0.0", "192.168.1.1", "10.67.0.1"} {
		notBypassed(&socks5Lib.AddrSpec{IP: net.ParseIP(ip), Port: 80})
		notBypassed(&socks5Lib.AddrSpec{FQDN: ip, Port: 80})
	}

	// Other names are not resolved here, whatever they point to.
	for _, name := range []string{"example.com", "localhost.example.com", "awl.example.com"} {
		notBypassed(&socks5Lib.AddrSpec{FQDN: name, Port: 80})
	}
}

// TestBypassOnlyForLoopbackClient: a client on this host connects to its
// localhost directly, while a client from the LAN gets it from the exit node.
func TestBypassOnlyForLoopbackClient(t *testing.T) {
	target := net.JoinHostPort("127.0.0.1", listenLoopback(t, "tcp", "127.0.0.1:0"))
	listenAddr := pickFreeAddr(t)
	client, err := NewClient(listenAddr, "", "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	dialer, err := proxy.SOCKS5("tcp", listenAddr, nil, nil)
	require.NoError(t, err)

	// dial connects to target through the proxy and reports whether the
	// request went to the exit node, which is not there.
	dial := func(fromLAN bool) (forwarded bool, err error) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn := <-client.ConnsChan()
			defer conn.Close()
			if fromLAN {
				conn = lanConn{Conn: conn}
			}
			_ = client.ServeConn(context.Background(), conn, func(context.Context, net.Conn, []byte) error {
				forwarded = true
				return errors.New("no exit node")
			})
		}()

		conn, err := dialer.Dial("tcp", target)
		if err == nil {
			_ = conn.Close()
		}
		<-done
		return forwarded, err
	}

	forwarded, err := dial(false)
	require.NoError(t, err)
	require.False(t, forwarded)

	forwarded, err = dial(true)
	require.ErrorContains(t, err, "general SOCKS server failure")
	require.True(t, forwarded)
}

// lanConn is a connection that seems to come from a device on the LAN.
type lanConn struct {
	net.Conn
}

func (c lanConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 50000}
}

// TestReadRequest: the request goes to the exit node as the client sent it,
// and what the client sends after it stays in the connection.
func TestReadRequest(t *testing.T) {
	port := []byte{0, 80}
	for name, req := range map[string][]byte{
		"IPv4": slices.Concat([]byte{5, 1, 0, socks5Lib.AddressIPv4}, netip.MustParseAddr("8.8.8.8").AsSlice(), port),
		"IPv6": slices.Concat([]byte{5, 1, 0, socks5Lib.AddressIPv6}, netip.MustParseAddr("2001:db8::1").AsSlice(), port),
		"name": slices.Concat([]byte{5, 1, 0, socks5Lib.AddressDomainName, 11}, []byte("example.com"), port),
	} {
		data := []byte("GET / HTTP/1.1\r\n")
		r := bytes.NewReader(slices.Concat(req, data))

		parsed, raw, err := readRequest(r)
		require.NoError(t, err, name)
		require.Equal(t, req, raw, name)
		require.Equal(t, socks5Lib.CommandConnect, parsed.Command, name)
		require.Equal(t, 80, parsed.DestAddr.Port, name)

		rest, err := io.ReadAll(r)
		require.NoError(t, err, name)
		require.Equal(t, data, rest, name)
	}
}
