package p2p

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	awlmetrics "github.com/anywherelan/awl/metrics"
)

// The hosts here have no bootstrap peers and nothing in their routing tables,
// so the DHT cannot find anyone, and nothing but redialOnDisconnect ever dials.

func TestRedialOnDisconnect(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	b := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	a.maxRedialDelay = 20 * time.Millisecond

	stop := a.redialOnDisconnect(t.Context(), func(id peer.ID) bool { return id == b.PeerID() })
	t.Cleanup(stop)

	oldConn := connectTCP(t, a, b, "/ip4/127.0.0.1")
	require.NoError(t, a.Host().Network().ClosePeer(b.PeerID()))

	// With no one to ask in the DHT, the addresses can only come from the peerstore
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(a.Host().Network().ConnsToPeer(b.PeerID()), func(conn network.Conn) bool {
			return conn.ID() != oldConn.ID()
		})
	}, 5*time.Second, 10*time.Millisecond)
}

func TestRedial_FailureIsNotCounted(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	gone := unreachablePeer(t)
	a.Host().Peerstore().AddAddrs(gone.ID, gone.Addrs, peerstore.TempAddrTTL)
	// What the schedule remembers from before the peer was connected
	a.retries.failed(gone.ID, time.Now(), time.Hour, errUnreachable)
	failuresBefore := metricValue(t, "awl_p2p_redials_total", redialFailure)
	dialsBefore := metricValue(t, "awl_p2p_redial_duration_seconds", redialFailure)

	calls := 0
	a.redial(t.Context(), gone.ID, 0, func(id peer.ID) bool {
		calls++
		return id == gone.ID
	})
	require.Equal(t, 2, calls, "the redial did not get as far as the dial")
	require.False(t, a.IsConnected(gone.ID))

	// The next round is to try the peer at once, not after a pause
	require.True(t, a.retries.due(gone.ID, time.Now()))

	require.Equal(t, failuresBefore+1, metricValue(t, "awl_p2p_redials_total", redialFailure))
	require.Equal(t, dialsBefore+1, metricValue(t, "awl_p2p_redial_duration_seconds", redialFailure))
}

func TestRedialDelay(t *testing.T) {
	p := &P2p{maxRedialDelay: 3 * time.Second, relayRedialWait: 5 * time.Second}
	relay := multiaddr.StringCast("/ip4/192.0.2.1/udp/6150/quic-v1/p2p-circuit")
	direct := multiaddr.StringCast("/ip4/192.0.2.1/udp/6150/quic-v1")

	// through a relay the side that accepted the connection dials first
	require.Zero(t, p.redialDelay(fakeConn{addr: relay, dir: network.DirInbound}))
	require.Equal(t, p.relayRedialWait, p.redialDelay(fakeConn{addr: relay, dir: network.DirOutbound}))
	// a direct connection: a random pause for both sides
	require.Less(t, p.redialDelay(fakeConn{addr: direct, dir: network.DirInbound}), p.maxRedialDelay)
	require.Less(t, p.redialDelay(fakeConn{addr: direct, dir: network.DirOutbound}), p.maxRedialDelay)
}

type fakeConn struct {
	network.Conn
	addr multiaddr.Multiaddr
	dir  network.Direction
}

func (c fakeConn) RemoteMultiaddr() multiaddr.Multiaddr { return c.addr }

func (c fakeConn) Stat() network.ConnStats {
	return network.ConnStats{Stats: network.Stats{Direction: c.dir}}
}

// metricValue reads a metric of this process by the value of its only label: the
// value of a counter, the number of observations of a histogram. The registry
// is shared by all the tests of the package, so compare it before and after.
func metricValue(t *testing.T, name, labelValue string) float64 {
	t.Helper()
	families, err := awlmetrics.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.GetLabel()[0].GetValue() != labelValue {
				continue
			}
			if histogram := metric.GetHistogram(); histogram != nil {
				return float64(histogram.GetSampleCount())
			}
			return metric.GetCounter().GetValue()
		}
	}
	return 0
}

func TestRedialOnDisconnect_SkipsNotKnownPeer(t *testing.T) {
	tests := []struct {
		name string
		// knownCalls is how many times the peer is still reported as known
		knownCalls int32
	}{
		{name: "never was known", knownCalls: 0},
		{name: "removed during the pause", knownCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
			b := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
			a.maxRedialDelay = 20 * time.Millisecond

			var calls atomic.Int32
			stop := a.redialOnDisconnect(t.Context(), func(id peer.ID) bool {
				return calls.Add(1) <= tt.knownCalls && id == b.PeerID()
			})
			t.Cleanup(stop)

			connectTCP(t, a, b, "/ip4/127.0.0.1")
			require.NoError(t, a.Host().Network().ClosePeer(b.PeerID()))

			require.Never(t, func() bool {
				return a.IsConnected(b.PeerID())
			}, 500*time.Millisecond, 10*time.Millisecond)
			require.Greater(t, calls.Load(), tt.knownCalls, "the redial did not get as far as the check")
		})
	}
}
