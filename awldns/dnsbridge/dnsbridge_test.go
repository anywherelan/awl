package dnsbridge_test

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/anywherelan/awl/awldns/dnsbridge"
)

const (
	testMTU = 3500
	// answerIP is what the test DNS handler returns for any A question.
	answerIP = "10.66.0.2"
)

var (
	clientIP = netip.MustParseAddr("10.66.0.1")
	dnsIP    = netip.MustParseAddr("10.66.255.254")
)

// testEnv wires a Bridge to a second netstack acting as the querying client:
// packets the client stack emits are fed into Bridge.HandlePacket (through a
// reused buffer — exercising HandlePacket's copy semantics), and packets the
// bridge emits are injected back into the client stack. A dns.Server with a
// static A-record handler serves the bridge listeners, mirroring how awldns
// will use them.
type testEnv struct {
	bridge    *dnsbridge.Bridge
	clientNet *netstack.Net
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	clientDev, clientNet, err := netstack.CreateNetTUN([]netip.Addr{clientIP}, nil, testMTU)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientDev.Close() })

	bridge, err := dnsbridge.New(dnsIP, testMTU, func(packet []byte) error {
		_, writeErr := clientDev.Write([][]byte{bytes.Clone(packet)}, 0)
		return writeErr
	})
	require.NoError(t, err)
	t.Cleanup(bridge.Close)

	// Client stack → bridge. The single read buffer is deliberately reused
	// between iterations: HandlePacket must copy the data.
	go func() {
		bufs := [][]byte{make([]byte, testMTU)}
		sizes := make([]int, 1)
		for {
			n, readErr := clientDev.Read(bufs, sizes, 0)
			if readErr != nil {
				return
			}
			for i := 0; i < n; i++ {
				bridge.HandlePacket(bufs[i][:sizes[i]])
			}
		}
	}()

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		for _, q := range r.Question {
			if q.Qtype != dns.TypeA {
				continue
			}
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP(answerIP).To4(),
			})
		}
		_ = w.WriteMsg(m)
	})

	udpServer := &dns.Server{PacketConn: bridge.UDPConn(), Handler: handler}
	go func() { _ = udpServer.ActivateAndServe() }()
	tcpServer := &dns.Server{Listener: bridge.TCPListener(), Handler: handler}
	go func() { _ = tcpServer.ActivateAndServe() }()
	t.Cleanup(func() {
		_ = udpServer.Shutdown()
		_ = tcpServer.Shutdown()
	})

	return &testEnv{bridge: bridge, clientNet: clientNet}
}

func (e *testEnv) exchange(t *testing.T, network, domain string) *dns.Msg {
	t.Helper()

	var conn net.Conn
	var err error
	switch network {
	case "udp":
		conn, err = e.clientNet.DialUDPAddrPort(netip.AddrPort{}, netip.AddrPortFrom(dnsIP, 53))
	case "tcp":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err = e.clientNet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(dnsIP, 53))
	default:
		t.Fatalf("unknown network %q", network)
	}
	require.NoError(t, err)
	defer conn.Close()

	dnsConn := &dns.Conn{Conn: conn}
	require.NoError(t, dnsConn.SetDeadline(time.Now().Add(5*time.Second)))

	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	require.NoError(t, dnsConn.WriteMsg(query))
	resp, err := dnsConn.ReadMsg()
	require.NoError(t, err)
	require.Equal(t, query.Id, resp.Id)

	return resp
}

func TestExchangeUDP(t *testing.T) {
	env := newTestEnv(t)

	resp := env.exchange(t, "udp", "peer.awl")
	require.Len(t, resp.Answer, 1)
	require.Equal(t, answerIP, resp.Answer[0].(*dns.A).A.String())
}

func TestExchangeTCP(t *testing.T) {
	env := newTestEnv(t)

	resp := env.exchange(t, "tcp", "peer.awl")
	require.Len(t, resp.Answer, 1)
	require.Equal(t, answerIP, resp.Answer[0].(*dns.A).A.String())
}

func TestSequentialQueries(t *testing.T) {
	env := newTestEnv(t)

	// Several exchanges through the same reused client-side read buffer:
	// catches data corruption if HandlePacket ever stops copying.
	for i := 0; i < 10; i++ {
		resp := env.exchange(t, "udp", "peer.awl")
		require.Len(t, resp.Answer, 1)
		require.Equal(t, answerIP, resp.Answer[0].(*dns.A).A.String())
	}
}

// TestClosedPortRejectedQuickly verifies the stack answers TCP SYN to a
// non-listening port with RST instead of silently dropping it — this is what
// makes Android Private DNS "Automatic" probes (DoT on :853) fail fast and
// fall back to plain DNS.
func TestClosedPortRejectedQuickly(t *testing.T) {
	env := newTestEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	conn, err := env.clientNet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(dnsIP, 853))
	elapsed := time.Since(start)

	require.Error(t, err)
	if conn != nil {
		_ = conn.Close()
	}
	require.NoError(t, ctx.Err(), "expected connection refused, got timeout after %s", elapsed)
	require.Less(t, elapsed, 2*time.Second)
}

func TestCloseIdempotentAndSafe(t *testing.T) {
	bridge, err := dnsbridge.New(dnsIP, testMTU, func([]byte) error { return nil })
	require.NoError(t, err)

	bridge.Close()
	bridge.Close()
	// Packets after close are silently dropped.
	bridge.HandlePacket([]byte{0x45, 0x00})
}

func TestDNSIP(t *testing.T) {
	bridge, err := dnsbridge.New(dnsIP, testMTU, func([]byte) error { return nil })
	require.NoError(t, err)
	t.Cleanup(bridge.Close)

	require.Equal(t, dnsIP, bridge.DNSIP())
}
