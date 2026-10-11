package p2p

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The hosts here have no bootstrap peers and nothing in their routing tables,
// so the DHT cannot find anyone, and nothing but redialOnDisconnect ever dials.

func TestRedialOnDisconnect(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	b := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	a.maxRedialDelay = 20 * time.Millisecond

	stop := a.redialOnDisconnect(t.Context(), func() []KnownPeer {
		return []KnownPeer{{ID: b.PeerID()}}
	})
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
	a.maxRedialDelay = 20 * time.Millisecond
	gone := unreachablePeer(t)
	a.Host().Peerstore().AddAddrs(gone.ID, gone.Addrs, peerstore.TempAddrTTL)
	// What the schedule remembers from before the peer was connected
	a.retries.failed(gone.ID, time.Now(), time.Hour, errUnreachable)
	failuresBefore := metricValue(t, "awl_p2p_redials_total", redialFailure)
	dialsBefore := metricValue(t, "awl_p2p_redial_duration_seconds", redialFailure)

	calls := 0
	a.redial(t.Context(), gone.ID, func() []KnownPeer {
		calls++
		return []KnownPeer{{ID: gone.ID}}
	})
	require.Equal(t, 2, calls, "the redial did not get as far as the dial")
	require.False(t, a.IsConnected(gone.ID))

	// The next round is to try the peer at once, not after a pause
	require.True(t, a.retries.due(gone.ID, time.Now()))

	require.Equal(t, failuresBefore+1, metricValue(t, "awl_p2p_redials_total", redialFailure))
	require.Equal(t, dialsBefore+1, metricValue(t, "awl_p2p_redial_duration_seconds", redialFailure))
}

// metricValue reads a metric of this process by the value of its only label: the
// value of a counter, the number of observations of a histogram. The registry
// is shared by all the tests of the package, so compare it before and after.
func metricValue(t *testing.T, name, labelValue string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
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
			stop := a.redialOnDisconnect(t.Context(), func() []KnownPeer {
				if calls.Add(1) > tt.knownCalls {
					return nil
				}
				return []KnownPeer{{ID: b.PeerID()}}
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
