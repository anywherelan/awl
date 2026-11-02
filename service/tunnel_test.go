package service

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/metrics"
	"github.com/anywherelan/awl/vpn"
)

// hotPathCases are our address and the peer's address in newTestTunnel.
var hotPathCases = []struct {
	name, ours, theirs string
}{
	{"IPv4", "10.66.0.1", "10.66.0.2"},
	{"IPv6", "fd00:66::1", "fd00:66::2"},
}

// TestTunnelHotPathDoesNotAllocate pins that routing a unicast packet to a peer
// and rewriting one received from it do not allocate.
func TestTunnelHotPathDoesNotAllocate(t *testing.T) {
	tt := newTestTunnel(t)

	for _, tc := range hotPathCases {
		t.Run("Outbound"+tc.name, func(t *testing.T) {
			pkt := udpPacket(t, tc.ours, tc.theirs)
			packets := make([]*vpn.Packet, 1)
			allocs := testing.AllocsPerRun(100, func() {
				packets[0] = pkt
				if !tt.routeToPeer(packets) {
					t.Fatal("packet was not routed to the peer")
				}
			})
			require.Zero(t, allocs)
		})
		t.Run("Inbound"+tc.name, func(t *testing.T) {
			packets := []*vpn.Packet{udpPacket(t, tc.theirs, tc.ours)}
			bufs := make([][]byte, 0, 1)
			senderIP := *tt.peer.localIP.Load()
			var err error
			allocs := testing.AllocsPerRun(100, func() {
				err = tt.writeInboundBatch(packets, bufs, senderIP, tt.peer)
			})
			require.NoError(t, err)
			require.Len(t, tt.tun.written, 1, "packet was not written to the TUN")
			require.Zero(t, allocs)
		})
	}
}

// TestTunnelKeepsAddressFamily checks that an IPv6 packet to the IPv4-mapped
// form of a peer's IPv4 address is not routed to that peer.
func TestTunnelKeepsAddressFamily(t *testing.T) {
	tt := newTestTunnel(t)
	packets := []*vpn.Packet{udpPacket(t, "fd00:66::1", "::ffff:10.66.0.2")}
	require.False(t, tt.routeToPeer(packets))
	require.NotNil(t, packets[0], "the packet stays with the caller")
}

// TestTunnelCountsDroppedPackets checks the reason counted for a packet read
// from the TUN that goes nowhere.
func TestTunnelCountsDroppedPackets(t *testing.T) {
	tests := []struct {
		name    string
		dst     string
		gateway bool
		// empty when the packet is not counted
		reason string
	}{
		{name: "unknown address", dst: "10.66.0.9", reason: "unknown_address"},
		{name: "unknown address IPv6", dst: "fd00:66::9", reason: "unknown_address"},
		{name: "unknown address with gateway", dst: "10.66.0.9", gateway: true, reason: "unknown_address"},
		{name: "internet without gateway", dst: "1.1.1.1", reason: "gateway_client_disabled"},
		{name: "multicast", dst: "224.0.0.251"},
		{name: "IPv6 multicast", dst: "ff02::fb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := newTestTunnel(t)
			if tc.gateway {
				tt.vpnGatewayClientEnabled = true
				tt.vpnGatewayPeer = tt.peer
			}
			src := "10.66.0.1"
			if netip.MustParseAddr(tc.dst).Is6() {
				src = "fd00:66::1"
			}

			want := droppedPackets(t)
			if tc.reason != "" {
				want[tc.reason]++
			}
			require.False(t, tt.routeToPeer([]*vpn.Packet{udpPacket(t, src, tc.dst)}))
			require.Equal(t, want, droppedPackets(t))
		})
	}
}

// TestTunnelCountsIPv6Disabled checks that an IPv6 packet from a peer is counted as
// dropped when it cannot be rewritten to our addresses.
func TestTunnelCountsIPv6Disabled(t *testing.T) {
	tests := []struct {
		name  string
		setup func(tt *testTunnel)
	}{
		{"IPv6 off on our side", func(tt *testTunnel) { tt.awlSubnet6 = netip.Prefix{} }},
		{"no IPv6 address of the sender", func(tt *testTunnel) { tt.peer.localIPv6.Store(nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := newTestTunnel(t)
			tc.setup(tt)

			want := droppedPackets(t)
			want["ipv6_disabled"]++
			packets := []*vpn.Packet{udpPacket(t, "fd00:66::2", "fd00:66::1")}
			require.NoError(t, tt.writeInboundBatch(packets, nil, *tt.peer.localIP.Load(), tt.peer))
			require.Empty(t, tt.tun.written)
			require.Equal(t, want, droppedPackets(t))
		})
	}
}

// droppedPackets returns awl_vpn_packets_dropped_total by reason. The registry
// is shared by all the tests of the package, so compare it before and after.
func droppedPackets(t *testing.T) map[string]float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	values := make(map[string]float64)
	for _, family := range families {
		if family.GetName() != "awl_vpn_packets_dropped_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			values[metric.GetLabel()[0].GetValue()] = metric.GetCounter().GetValue()
		}
	}
	return values
}

// BenchmarkHandleReadPackets measures routing one unicast packet to a peer.
func BenchmarkHandleReadPackets(b *testing.B) {
	for _, tc := range hotPathCases {
		b.Run(tc.name, func(b *testing.B) {
			tt := newTestTunnel(b)
			pkt := udpPacket(b, tc.ours, tc.theirs)
			packets := make([]*vpn.Packet, 1)

			b.ReportAllocs()
			for b.Loop() {
				packets[0] = pkt
				if !tt.routeToPeer(packets) {
					b.Fatal("packet was not routed to the peer")
				}
			}
		})
	}
}

// testTunnel is a Tunnel over a fake TUN, with our addresses 10.66.0.1/16 and
// fd00:66::1/48 and one peer, 10.66.0.2 and fd00:66::2. VpnPeers run no
// outbound senders, so the packets routed to the peer stay in its outboundCh.
type testTunnel struct {
	*Tunnel
	tun  *fakeTUN
	peer *VpnPeer
}

func newTestTunnel(tb testing.TB) *testTunnel {
	tb.Helper()
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(tb, err)
	peerID, err := peer.IDFromPrivateKey(key)
	require.NoError(tb, err)

	tb.Setenv(config.AppDataDirEnvKey, tb.TempDir())
	conf := config.NewConfig(config.AppTypeAwl, eventbus.NewBus())
	tb.Cleanup(conf.Close)
	conf.Lock()
	conf.VPNConfig.IPNet = "10.66.0.1/16"
	conf.VPNConfig.IPNetV6 = "fd00:66::1/48"
	conf.P2pNode.ParallelSendingStreamsCount = 0
	conf.KnownPeers[peerID.String()] = config.KnownPeer{
		PeerID:            peerID.String(),
		IPAddr:            "10.66.0.2",
		IPAddrV6:          "fd00:66::2",
		RemoteIPv6Enabled: true,
	}
	conf.Unlock()

	fake := &fakeTUN{events: make(chan tun.Event)}
	device, err := vpn.NewDevice(fake, "fake", netip.Prefix{}, netip.Prefix{})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = device.Close() })

	tunnel := NewTunnel(fakeP2p{}, device, conf, eventbus.NewBus())
	tb.Cleanup(tunnel.Close)

	return &testTunnel{Tunnel: tunnel, tun: fake, peer: tunnel.peerIDToPeer[peerID]}
}

// routeToPeer passes packets to HandleReadPackets and reports whether a packet
// was routed to the peer.
func (tt *testTunnel) routeToPeer(packets []*vpn.Packet) bool {
	tt.HandleReadPackets(packets)
	select {
	case <-tt.peer.outboundCh:
		return true
	default:
		return false
	}
}

// fakeP2p is a P2p without a network. It implements only what the tunnel calls
// when no outbound senders run; anything else panics on the nil P2p.
type fakeP2p struct{ P2p }

func (fakeP2p) SubscribeConnectionEvents(_, _ func(network.Network, network.Conn)) {}

// fakeTUN is a tun.Device that keeps the last batch written to it. It
// implements only what vpn.Device calls; anything else panics on the nil
// tun.Device.
type fakeTUN struct {
	tun.Device
	events  chan tun.Event
	written [][]byte // reused, so that writing does not allocate
}

func (f *fakeTUN) Write(bufs [][]byte, _ int) (int, error) {
	f.written = append(f.written[:0], bufs...)
	return len(bufs), nil
}

func (f *fakeTUN) MTU() (int, error) { return vpn.InterfaceMTU, nil }

func (f *fakeTUN) Events() <-chan tun.Event { return f.events }

func (f *fakeTUN) BatchSize() int { return 128 }

func (f *fakeTUN) Close() error {
	close(f.events)
	return nil
}

// udpPacket returns a parsed UDP packet from src to dst. The IP version follows
// src; dst may be an IPv4-mapped address in an IPv6 packet.
func udpPacket(tb testing.TB, src, dst string) *vpn.Packet {
	tb.Helper()
	srcAddr := netip.MustParseAddr(src)
	template := "4500002828f540004011fd490a4200010a420002a9d0238200148bfd68656c6c6f20776f726c6421"
	if srcAddr.Is6() {
		template = "6000000000141140fd000000000000000000000000000001fd00000000000000000000000000000204d2162e0014000068656c6c6f20776f726c6421"
	}
	raw, err := hex.DecodeString(template)
	require.NoError(tb, err)

	pkt := new(vpn.Packet)
	_, err = pkt.ReadFrom(bytes.NewReader(raw))
	require.NoError(tb, err)
	require.True(tb, pkt.Parse())
	pkt.SetSrc(srcAddr)
	pkt.SetDst(netip.MustParseAddr(dst))
	pkt.RecalculateChecksum()
	return pkt
}
