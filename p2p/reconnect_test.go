package p2p

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// The hosts here have nothing in their routing tables, so looking a peer up in
// the DHT fails at once and a round over unreachable peers takes no time.

func addrInfo(p *P2p) peer.AddrInfo {
	return peer.AddrInfo{ID: p.PeerID(), Addrs: p.Host().Addrs()}
}

// unreachablePeer is a peer nobody listens for: a dial is refused right away.
func unreachablePeer(t *testing.T) peer.AddrInfo {
	return peer.AddrInfo{
		ID:    test.RandPeerIDFatal(t),
		Addrs: []multiaddr.Multiaddr{multiaddr.StringCast("/ip4/127.0.0.1/tcp/1")},
	}
}

func TestConnectToKnownPeers_BacksOff(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	offline := KnownPeer{ID: test.RandPeerIDFatal(t)}
	eager := KnownPeer{ID: test.RandPeerIDFatal(t), Eager: true}

	const base = 50 * time.Millisecond
	rounds := 0
	for start := time.Now(); time.Since(start) < 20*base; rounds++ {
		a.connectToKnownPeers(t.Context(), time.Second, base, []KnownPeer{offline, eager}, false)
		time.Sleep(base / 10)
	}

	require.Greater(t, rounds, 50)
	require.Equal(t, rounds, a.retries.failuresOf(eager.ID))
	// The pauses of base, 2, 4 and 8 times base fit into the 20, the one of 16 does not
	require.InDelta(t, 5, a.retries.failuresOf(offline.ID), 1)
}

func TestReconnectInfo(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	offline := KnownPeer{ID: test.RandPeerIDFatal(t)}
	require.Nil(t, a.ReconnectInfo(offline.ID), "nothing is held against a peer that was not tried yet")

	a.connectToKnownPeers(t.Context(), time.Second, time.Hour, []KnownPeer{offline}, false)
	info := a.ReconnectInfo(offline.ID)
	require.NotNil(t, info)
	require.Equal(t, 1, info.Failures)
	require.NotEmpty(t, info.LastError)
	require.Less(t, time.Duration(info.LastAttemptAgo), time.Minute)
	// an hour is over the cap: the pause is maxRetryInterval, shortened by up to 20%
	require.Greater(t, time.Duration(info.NextAttemptIn), maxRetryInterval*7/10)
	require.LessOrEqual(t, time.Duration(info.NextAttemptIn), maxRetryInterval)
}

func TestConnectToKnownPeers_SkipsPeerBeingRedialed(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	offline := KnownPeer{ID: test.RandPeerIDFatal(t)}
	round := func() {
		a.connectToKnownPeers(t.Context(), time.Second, time.Hour, []KnownPeer{offline}, true)
	}

	a.redials.Store(offline.ID, struct{}{})
	round()
	require.Zero(t, a.retries.failuresOf(offline.ID))

	a.redials.Delete(offline.ID)
	round()
	require.Equal(t, 1, a.retries.failuresOf(offline.ID))
}

func TestConnectToKnownPeers_ForceClearsSwarmBackoff(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	backoff := a.Host().Network().(*swarm.Swarm).Backoff()
	// The DHT finds no one here, so a round dials neither peer and adds no
	// backoff of its own
	offline, eager := unreachablePeer(t), unreachablePeer(t)
	backoff.AddBackoff(offline.ID, offline.Addrs[0])
	backoff.AddBackoff(eager.ID, eager.Addrs[0])
	knownPeers := []KnownPeer{{ID: offline.ID}, {ID: eager.ID, Eager: true}}

	// Being eager gets a peer an attempt every round, but not a clean slate
	a.connectToKnownPeers(t.Context(), time.Second, time.Hour, knownPeers, false)
	require.Equal(t, 1, a.retries.failuresOf(eager.ID))
	require.True(t, backoff.Backoff(offline.ID, offline.Addrs[0]))
	require.True(t, backoff.Backoff(eager.ID, eager.Addrs[0]))

	a.connectToKnownPeers(t.Context(), time.Second, time.Hour, knownPeers, true)
	require.False(t, backoff.Backoff(offline.ID, offline.Addrs[0]))
	require.False(t, backoff.Backoff(eager.ID, eager.Addrs[0]))
}

func TestConnectToKnownPeers_Bootstrap(t *testing.T) {
	alive := []*P2p{
		newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0"),
		newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0"),
	}
	dead := unreachablePeer(t)
	a := newTestP2pWithBootstrap(t, nil, "/ip4/127.0.0.1/tcp/0", []peer.AddrInfo{
		addrInfo(alive[0]), addrInfo(alive[1]), dead,
	})
	// With a pause this long nobody gets a second turn by the schedule
	round := func() (bootstrapBefore, bootstrapAfter int) {
		return a.connectToKnownPeers(t.Context(), 5*time.Second, time.Hour, nil, false)
	}

	_, connected := round()
	require.Equal(t, 2, connected)
	require.Equal(t, 1, a.retries.failuresOf(dead.ID))
	deadInfo := a.BootstrapPeersStatsDetailed()[dead.ID.String()]
	require.NotEmpty(t, deadInfo.Error)
	require.Equal(t, 1, deadInfo.Reconnect.Failures)
	require.Equal(t, deadInfo.Error, deadInfo.Reconnect.LastError)
	// The address of the dead one is known all the same, it just does not answer
	require.Contains(t, deadInfo.PeerstoreAddrs, dead.Addrs[0].String())
	for _, peer := range alive {
		info := a.BootstrapPeersStatsDetailed()[peer.PeerID().String()]
		require.Nil(t, info.Reconnect)
		require.Contains(t, info.PeerstoreAddrs, addrInfo(peer).Addrs[0].String())
	}

	// Enough of them are connected: the dead one waits for its turn, and what
	// its last attempt reported stays on record
	round()
	round()
	require.Equal(t, 1, a.retries.failuresOf(dead.ID))
	require.Equal(t, deadInfo.Error, a.BootstrapPeersStatsDetailed()[dead.ID.String()].Error)

	// One more is gone and they are not enough: back to trying every round
	require.NoError(t, alive[1].Close())
	require.Eventually(t, func() bool {
		return !a.IsConnected(alive[1].PeerID())
	}, 5*time.Second, 10*time.Millisecond)
	round()
	before, after := round()
	require.Equal(t, 1, before)
	require.Equal(t, 1, after)
	require.Equal(t, 3, a.retries.failuresOf(dead.ID))
	require.Equal(t, 2, a.retries.failuresOf(alive[1].PeerID()))
}

func TestMaintainBackgroundConnections_NetworkReturns(t *testing.T) {
	bootstrap := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	bootstrapInfo := addrInfo(bootstrap)
	// A bootstrap peer that does not listen is what a lost network looks like
	bootstrapSwarm := bootstrap.Host().Network().(*swarm.Swarm)
	listenAddrs := bootstrapSwarm.ListenAddresses()
	bootstrapSwarm.ListenClose(listenAddrs...)

	a := newTestP2pWithBootstrap(t, nil, "/ip4/127.0.0.1/tcp/0", []peer.AddrInfo{bootstrapInfo})
	a.firstRetryDelay = 10 * time.Millisecond
	close(a.dhtBootstrapFinishedChan)
	offline := KnownPeer{ID: test.RandPeerIDFatal(t)}

	go a.MaintainBackgroundConnections(t.Context(), 20*time.Millisecond, func() []KnownPeer {
		return []KnownPeer{offline}
	})

	const manyFailures = 5
	require.Eventually(t, func() bool {
		return a.retries.failuresOf(offline.ID) >= manyFailures
	}, 10*time.Second, 5*time.Millisecond)

	resetsBefore := metricValue(t, "awl_p2p_reconnect_resets_total", networkBootstrapReturned)
	require.NoError(t, bootstrapSwarm.Listen(listenAddrs...))

	// Failures only add up, unless the schedule was reset
	require.Eventually(t, func() bool {
		return a.IsConnected(bootstrap.PeerID()) && a.retries.failuresOf(offline.ID) < manyFailures
	}, 10*time.Second, time.Millisecond)
	require.Greater(t, metricValue(t, "awl_p2p_reconnect_resets_total", networkBootstrapReturned), resetsBefore)
}

func TestMaintainBackgroundConnections_NotifyNetworkChanged(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	a.firstRetryDelay = 10 * time.Millisecond
	close(a.dhtBootstrapFinishedChan)
	offline := KnownPeer{ID: test.RandPeerIDFatal(t)}

	// With an interval this long the only rounds are the two of a start
	var rounds atomic.Int32
	go a.MaintainBackgroundConnections(t.Context(), time.Hour, func() []KnownPeer {
		rounds.Add(1)
		return []KnownPeer{offline}
	})
	startedAnew := func(wantRounds int32) func() bool {
		return func() bool {
			return rounds.Load() == wantRounds && a.retries.failuresOf(offline.ID) == 2
		}
	}
	require.Eventually(t, startedAnew(2), 10*time.Second, 5*time.Millisecond)

	// As if the peer has been failing for long
	for range 5 {
		a.retries.failed(offline.ID, time.Now(), time.Hour, errUnreachable)
	}

	resetsBefore := metricValue(t, "awl_p2p_reconnect_resets_total", networkNotified)
	a.NotifyNetworkChanged()
	require.Eventually(t, startedAnew(4), 10*time.Second, 5*time.Millisecond)
	require.Equal(t, resetsBefore+1, metricValue(t, "awl_p2p_reconnect_resets_total", networkNotified))
}
