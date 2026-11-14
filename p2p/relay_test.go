package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/x/simlibp2p"
	"github.com/marcopolo/simnet"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// knownPeersSet is the list of known peers of one host.
type knownPeersSet struct {
	mu  sync.Mutex
	ids map[peer.ID]bool
}

func (s *knownPeersSet) add(ids ...peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = make(map[peer.ID]bool)
	}
	for _, id := range ids {
		s.ids[id] = true
	}
}

func (s *knownPeersSet) remove(id peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

func (s *knownPeersSet) isKnown(id peer.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids[id]
}

// TestKnownPeersRelay runs on a simulated network: R relays for known peers, A
// and B know each other and R, and they can't connect directly, as if both were
// behind NAT. C is not known to R.
func TestKnownPeersRelay(t *testing.T) {
	simNet := &simnet.Simnet{LatencyFunc: simnet.StaticLatency(time.Millisecond)}
	simNet.Start()
	t.Cleanup(simNet.Close)
	link := simnet.NodeBiDiLinkSettings{
		Downlink: simnet.LinkSettings{BitsPerSecond: 100 * simlibp2p.OneMbps},
		Uplink:   simnet.LinkSettings{BitsPerSecond: 100 * simlibp2p.OneMbps},
	}

	var relayKnows knownPeersSet
	r := newSimnetP2p(t, simNet, link, "1.2.3.3",
		KnownPeersRelayService(relayKnows.isKnown),
		libp2p.ForceReachabilityPublic(),
	)
	b := newSimnetP2p(t, simNet, link, "1.2.3.11")
	a := newSimnetP2p(t, simNet, link, "1.2.3.10", libp2p.ConnectionGater(relayOnlyGater{peerID: b.PeerID()}))
	c := newSimnetP2p(t, simNet, link, "1.2.3.12")
	relayKnows.add(a.PeerID(), b.PeerID())
	for _, p := range []*P2p{a, b, c} {
		t.Cleanup(p.knownRelays.start())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	relayInfo := peer.AddrInfo{ID: r.PeerID(), Addrs: r.Host().Addrs()}
	for _, p := range []*P2p{a, b, c} {
		require.NoError(t, p.Host().Connect(ctx, relayInfo))
	}

	hasReservation := func(p *P2p) bool {
		return len(p.knownRelays.circuitAddrs("")) > 0
	}
	// R keeps A and B in its known peers. C thinks R does so with C too, as
	// it would before it learns that R has removed it.
	relayConfirmed := []KnownPeer{{ID: r.PeerID(), Confirmed: true}}
	reserved := func(peers ...*P2p) func() bool {
		return func() bool {
			ok := true
			for _, p := range peers {
				p.connectToKnownPeers(t.Context(), time.Second, time.Hour, relayConfirmed, false)
				ok = ok && hasReservation(p)
			}
			return ok
		}
	}
	require.Eventually(t, reserved(a, b), 5*time.Second, 50*time.Millisecond, "A and B must reserve a slot at R")
	require.Never(t, reserved(c), time.Second, 50*time.Millisecond, "R refuses C, it doesn't know C")

	// not without the confirmation: R would refuse
	d := newSimnetP2p(t, simNet, link, "1.2.3.13")
	relayKnows.add(d.PeerID())
	require.NoError(t, d.Host().Connect(ctx, relayInfo))
	require.Never(t, func() bool {
		d.connectToKnownPeers(t.Context(), time.Second, time.Hour, []KnownPeer{{ID: r.PeerID()}}, false)
		return hasReservation(d)
	}, time.Second, 50*time.Millisecond, "D must not reserve a slot at a peer that has not confirmed it")

	// C is not allowed to use the relay
	viaRelay := multiaddr.StringCast("/p2p/" + r.PeerID().String() + "/p2p-circuit")
	err := c.basicHost.Connect(network.WithAllowLimitedConn(ctx, "test"), peer.AddrInfo{ID: a.PeerID(), Addrs: []multiaddr.Multiaddr{viaRelay}})
	require.Error(t, err)
	require.False(t, c.IsConnected(a.PeerID()))

	require.NoError(t, a.ConnectPeer(ctx, b.PeerID()))
	conns := a.connsToPeer(b.PeerID())
	require.NotEmpty(t, conns)
	for _, conn := range conns {
		relayID, err := conn.RemoteMultiaddr().ValueForProtocol(multiaddr.P_P2P)
		require.NoError(t, err, conn.RemoteMultiaddr().String())
		require.Equal(t, r.PeerID().String(), relayID)
		require.False(t, conn.Stat().Limited, "the relay of known peers does not limit connections")
	}

	// more than a public relay lets through, 128 KiB
	const echoProtocol protocol.ID = "/awl/test/echo"
	b.Host().SetStreamHandler(echoProtocol, func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(s, s)
	})
	data := make([]byte, 1<<20)
	_, _ = rand.Read(data)
	s, err := a.NewStream(ctx, b.PeerID(), echoProtocol)
	require.NoError(t, err)
	go func() {
		_, _ = s.Write(data)
		_ = s.CloseWrite()
	}()
	echoed, err := io.ReadAll(s)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, echoed))

	// the relay drops our reservation along with the connection
	require.NoError(t, b.Host().Network().ClosePeer(r.PeerID()))
	require.Eventually(t, func() bool {
		return !hasReservation(b)
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, b.Host().Connect(ctx, relayInfo))
	require.Eventually(t, reserved(b), 5*time.Second, 50*time.Millisecond, "B must reserve a slot again with a new connection")

	// a peer removed on the relay can no longer reach the others through it
	relayKnows.remove(b.PeerID())
	require.NoError(t, a.Host().Network().ClosePeer(b.PeerID()))
	require.Eventually(t, func() bool {
		return !a.IsConnected(b.PeerID())
	}, 5*time.Second, 10*time.Millisecond)
	require.Error(t, a.ConnectPeer(ctx, b.PeerID()))
}

// relayOnlyGater lets a host connect to peerID only through a relay.
type relayOnlyGater struct {
	peerID peer.ID
}

func (g relayOnlyGater) InterceptPeerDial(peer.ID) bool {
	return true
}

func (g relayOnlyGater) InterceptAddrDial(id peer.ID, addr multiaddr.Multiaddr) bool {
	return id != g.peerID || isRelayAddr(addr)
}

func (g relayOnlyGater) InterceptAccept(network.ConnMultiaddrs) bool {
	return true
}

func (g relayOnlyGater) InterceptSecured(_ network.Direction, id peer.ID, addrs network.ConnMultiaddrs) bool {
	return id != g.peerID || isRelayAddr(addrs.RemoteMultiaddr())
}

func (g relayOnlyGater) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

func TestKnownRelaysCircuitAddrs(t *testing.T) {
	r := newKnownRelays(nil)
	relay1, relay2, target := test.RandPeerIDFatal(t), test.RandPeerIDFatal(t), test.RandPeerIDFatal(t)
	now := time.Now()
	r.reservations[relay1] = now.Add(time.Hour)
	r.reservations[relay2] = now.Add(-time.Second)
	r.reservations[target] = now.Add(time.Hour)

	addrs := r.circuitAddrs(target)
	require.Len(t, addrs, 1, "not an expired reservation, not through the peer itself")
	require.Equal(t, "/p2p/"+relay1.String()+"/p2p-circuit", addrs[0].String())
}
