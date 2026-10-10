package p2p

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// maxRetryInterval caps the pause between attempts to reach a peer that keeps
// failing. It is also the longest we stay unaware that such a peer is back, in
// the case when the peer cannot reach us itself.
const maxRetryInterval = 10 * time.Minute

// retryDelay is the pause before the next attempt after the given number of
// failures in a row: base, 2*base, 4*base and so on up to maxDelay, shortened by
// up to 20% so that peers which failed together do not come back together.
func retryDelay(failures int, base, maxDelay time.Duration) time.Duration {
	delay := base
	for i := 1; i < failures && delay < maxDelay; i++ {
		delay *= 2
	}
	delay = min(delay, maxDelay)

	return delay - rand.N(delay/5)
}

type retryState struct {
	failures int
	next     time.Time
}

// retrySchedule remembers the peers that could not be reached and when to try
// them again, so that a peer which is gone is not looked up in the DHT at a
// constant rate forever. The state lives in memory only: after a restart every
// peer is tried from scratch.
type retrySchedule struct {
	mu    sync.Mutex
	peers map[peer.ID]retryState
}

func newRetrySchedule() *retrySchedule {
	return &retrySchedule{peers: make(map[peer.ID]retryState)}
}

// due reports whether it is time to try the peer.
func (s *retrySchedule) due(id peer.ID, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return !now.Before(s.peers[id].next)
}

// failed records an attempt that did not reach the peer and puts the next one
// off.
func (s *retrySchedule) failed(id peer.ID, now time.Time, base time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.peers[id]
	state.failures++
	state.next = now.Add(retryDelay(state.failures, base, maxRetryInterval))
	s.peers[id] = state
}

// forget drops what is remembered about the peer.
func (s *retrySchedule) forget(id peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.peers, id)
}

// reset makes every peer due again, with its failures forgotten.
func (s *retrySchedule) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	clear(s.peers)
}
