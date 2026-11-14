package p2p

import (
	"context"
	"net"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/x/simlibp2p"
	"github.com/marcopolo/simnet"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestLANDialer_FailureMemory(t *testing.T) {
	l := newLANDialer(nil, nil)
	id := test.RandPeerIDFatal(t)
	addr1 := multiaddr.StringCast("/ip4/192.168.1.7/udp/4363/quic-v1")
	addr2 := multiaddr.StringCast("/ip4/192.168.1.7/tcp/4363")

	l.rememberFailure(lanDialKey(id, []multiaddr.Multiaddr{addr1, addr2}))
	require.True(t, l.failedRecently(lanDialKey(id, []multiaddr.Multiaddr{addr2, addr1})), "the same addresses in another order")
	require.False(t, l.failedRecently(lanDialKey(id, []multiaddr.Multiaddr{addr1})), "other addresses")

	l.forgetFailures()
	require.False(t, l.failedRecently(lanDialKey(id, []multiaddr.Multiaddr{addr1, addr2})), "after the network changed")
}

// TestLANDialer runs on a simulated network: a relay with a public address far
// away, and peers in one LAN next to each other that reach one another only
// through the relay.
func TestLANDialer(t *testing.T) {
	const relayIP = "1.2.3.3"
	simNet := &simnet.Simnet{LatencyFunc: func(packet *simnet.Packet) time.Duration {
		if packet.From.(*net.UDPAddr).IP.String() == relayIP || packet.To.(*net.UDPAddr).IP.String() == relayIP {
			return 20 * time.Millisecond
		}
		return time.Millisecond
	}}
	simNet.Start()
	t.Cleanup(simNet.Close)
	link := simnet.NodeBiDiLinkSettings{
		Downlink: simnet.LinkSettings{BitsPerSecond: 100 * simlibp2p.OneMbps},
		Uplink:   simnet.LinkSettings{BitsPerSecond: 100 * simlibp2p.OneMbps},
	}

	relayHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/"+relayIP+"/udp/1234/quic-v1"),
		libp2p.Transport(libp2pquic.NewTransport),
		simlibp2p.QUICSimnet(simNet, link),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = relayHost.Close() })
	_, err = relayv2.New(relayHost)
	require.NoError(t, err)
	relayInfo := peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}

	a := newSimnetP2p(t, simNet, link, "192.168.1.10")
	b := newSimnetP2p(t, simNet, link, "192.168.1.11")
	c := newSimnetP2p(t, simNet, link, "192.168.1.12")
	t.Cleanup(newLANDialer(a, func(id peer.ID) bool { return id == b.PeerID() }).start(t.Context()))
	t.Cleanup(newLANDialer(b, func(id peer.ID) bool { return id == a.PeerID() }).start(t.Context()))

	ctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(t.Context(), "test"), 10*time.Second)
	defer cancel()
	require.NoError(t, a.Host().Connect(ctx, relayInfo))
	_, err = client.Reserve(ctx, a.Host(), relayInfo)
	require.NoError(t, err)
	viaRelay := peer.AddrInfo{ID: a.PeerID(), Addrs: []multiaddr.Multiaddr{
		multiaddr.StringCast(relayInfo.Addrs[0].String() + "/p2p/" + relayHost.ID().String() + "/p2p-circuit"),
	}}

	// C is not known to A
	require.NoError(t, c.Host().Connect(ctx, viaRelay))
	require.NoError(t, b.Host().Connect(ctx, viaRelay))
	require.Nil(t, b.directConnToPeer(a.PeerID()), "B is to reach A only through the relay")

	require.Eventually(t, func() bool {
		return a.directConnToPeer(b.PeerID()) != nil
	}, 5*time.Second, 10*time.Millisecond)
	require.Nil(t, a.directConnToPeer(c.PeerID()))
}

// newSimnetP2p builds a host on simNet at ip, with an interface in a /24 LAN.
func newSimnetP2p(t *testing.T, simNet *simnet.Simnet, link simnet.NodeBiDiLinkSettings, ip string, opts ...libp2p.Option) *P2p {
	t.Helper()
	peerstore, err := pstoremem.NewPeerstore()
	require.NoError(t, err)

	p := NewP2p(context.Background())
	lan := testIface("eth0", testRunning, ip+"/24")
	p.localAddrs.read = func() ([]localInterface, error) { return []localInterface{lan}, nil }
	_, err = p.InitHost(HostConfig{
		ListenAddrs:              []multiaddr.Multiaddr{multiaddr.StringCast("/ip4/" + ip + "/udp/1234/quic-v1")},
		AllowEmptyBootstrapPeers: true,
		Peerstore:                peerstore,
		DHTDatastore:             dssync.MutexWrap(ds.NewMapDatastore()),
		DHTOpts:                  []dht.Option{dht.DisableAutoRefresh()},
		Libp2pOpts: append([]libp2p.Option{
			libp2p.EnableRelay(),
			simlibp2p.QUICSimnet(simNet, link),
		}, opts...),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}
