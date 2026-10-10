package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// failuresOf is how many times in a row the peer could not be reached.
func (s *retrySchedule) failuresOf(id peer.ID) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.peers[id].failures
}

func TestRetryDelay(t *testing.T) {
	const base = 20 * time.Second
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 20 * time.Second},
		{failures: 1, want: 20 * time.Second},
		{failures: 2, want: 40 * time.Second},
		{failures: 3, want: 80 * time.Second},
		{failures: 4, want: 160 * time.Second},
		{failures: 5, want: 320 * time.Second},
		{failures: 6, want: 10 * time.Minute},
		{failures: 7, want: 10 * time.Minute},
		// a peer can keep failing for months
		{failures: 1_000_000, want: 10 * time.Minute},
	}
	for _, tt := range tests {
		for range 100 {
			got := retryDelay(tt.failures, base, maxRetryInterval)
			require.LessOrEqual(t, got, tt.want, "failures: %d", tt.failures)
			require.GreaterOrEqual(t, got, tt.want*8/10, "failures: %d", tt.failures)
		}
	}
}

func TestRetrySchedule(t *testing.T) {
	const base = 20 * time.Second
	const id, otherID = peer.ID("peer"), peer.ID("other peer")
	now := time.Now()

	fail := func(s *retrySchedule, id peer.ID, times int) {
		for range times {
			s.failed(id, now, base)
		}
	}

	t.Run("each failure doubles the pause up to the cap", func(t *testing.T) {
		s := newRetrySchedule()
		at := now
		for _, pause := range []time.Duration{
			20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 320 * time.Second,
			maxRetryInterval, maxRetryInterval,
		} {
			require.True(t, s.due(id, at))
			s.failed(id, at, base)

			require.False(t, s.due(id, at.Add(pause*8/10-time.Second)), "pause: %s", pause)
			at = at.Add(pause)
		}
	})

	t.Run("forget", func(t *testing.T) {
		s := newRetrySchedule()
		fail(s, id, 5)
		fail(s, otherID, 5)
		s.forget(id)

		require.Zero(t, s.failuresOf(id))
		require.True(t, s.due(id, now))
		require.Equal(t, 5, s.failuresOf(otherID))
		require.False(t, s.due(otherID, now))
	})

	t.Run("reset", func(t *testing.T) {
		s := newRetrySchedule()
		fail(s, id, 5)
		fail(s, otherID, 5)
		s.reset()

		require.True(t, s.due(id, now))
		require.True(t, s.due(otherID, now))
		// and the count starts anew
		fail(s, id, 1)
		require.Equal(t, 1, s.failuresOf(id))
	})
}
