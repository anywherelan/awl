package p2p

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// defaultMaxRedialDelay bounds the random pause before a redial.
	defaultMaxRedialDelay = 3 * time.Second
	redialTimeout         = 5 * time.Second
)

// redialOnDisconnect reconnects to a known peer as soon as its last connection
// is gone, instead of leaving it to the next round of
// MaintainBackgroundConnections. A relay cuts a relayed connection every two
// minutes, so for a peer behind one this is a gap of a few seconds instead of a
// whole round, several hundred times a day.
//
// The returned func removes the subscription.
func (p *P2p) redialOnDisconnect(ctx context.Context, knownPeersIdsFunc func() []peer.ID) (stop func()) {
	notifee := &network.NotifyBundle{
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			// Fires for every connection of every peer, DHT servers included,
			// and the swarm waits for it to return.
			go p.redial(ctx, conn.RemotePeer(), knownPeersIdsFunc)
		},
	}
	p.host.Network().Notify(notifee)

	return func() { p.host.Network().StopNotify(notifee) }
}

func (p *P2p) redial(ctx context.Context, peerID peer.ID, knownPeersIdsFunc func() []peer.ID) {
	if p.IsConnected(peerID) || !slices.Contains(knownPeersIdsFunc(), peerID) {
		return
	}
	if _, pending := p.redials.LoadOrStore(peerID, struct{}{}); pending {
		return
	}
	defer p.redials.Delete(peerID)

	// Both sides see the connection go at the same moment and would dial each
	// other head-on every time. The pause is random rather than decided by peer
	// ID: who dials through a relay decides who then starts the upgrade to a
	// direct connection, and that should not be fixed for a pair of peers.
	timer := time.NewTimer(rand.N(p.maxRedialDelay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	// The other side may have dialed first, or the peer was removed meanwhile
	if p.IsConnected(peerID) || !slices.Contains(knownPeersIdsFunc(), peerID) {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, redialTimeout)
	defer cancel()
	// Unlike ConnectPeer, no DHT lookup first: p.host is a routed host, it dials
	// the addresses still in the peerstore and asks the DHT only if that fails.
	// We were connected a moment ago, the DHT cannot know anything newer.
	err := p.host.Connect(ctx, peer.AddrInfo{ID: peerID})
	if err != nil {
		p.logger.Debugf("redial %s after disconnect: %v", peerID, err)
	}
}
