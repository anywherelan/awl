package p2p

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// The hosts here have no bootstrap peers and nothing in their routing tables,
// so the DHT cannot find anyone, and nothing but redialOnDisconnect ever dials.

func TestRedialOnDisconnect(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	b := newTestP2p(t, nil, "/ip4/127.0.0.1/tcp/0")
	a.maxRedialDelay = 20 * time.Millisecond

	stop := a.redialOnDisconnect(t.Context(), func() []peer.ID { return []peer.ID{b.PeerID()} })
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
			stop := a.redialOnDisconnect(t.Context(), func() []peer.ID {
				if calls.Add(1) > tt.knownCalls {
					return nil
				}
				return []peer.ID{b.PeerID()}
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
