package p2p

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	awlmetrics "github.com/anywherelan/awl/metrics"
)

const (
	// defaultMaxRedialDelay bounds the random pause before a redial.
	defaultMaxRedialDelay = 3 * time.Second
	redialTimeout         = 5 * time.Second
	// defaultFirstRetryDelay is the pause between the two rounds made at the
	// start and whenever the network comes back.
	defaultFirstRetryDelay = 5 * time.Second

	// how a redial ended, a metric label
	redialSuccess       = "success"
	redialFailure       = "failure"
	redialPeerConnected = "peer_connected"
)

// redialOnDisconnect reconnects to a known peer as soon as its last connection
// is gone, instead of leaving it to the next round of
// MaintainBackgroundConnections. A relay cuts a relayed connection every two
// minutes, so for a peer behind one this is a gap of a few seconds instead of a
// whole round, several hundred times a day.
//
// The returned func removes the subscription.
func (p *P2p) redialOnDisconnect(ctx context.Context, knownPeersFunc func() []KnownPeer) (stop func()) {
	notifee := &network.NotifyBundle{
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			// Fires for every connection of every peer, DHT servers included,
			// and the swarm waits for it to return.
			go p.redial(ctx, conn.RemotePeer(), knownPeersFunc)
		},
	}
	p.host.Network().Notify(notifee)

	return func() { p.host.Network().StopNotify(notifee) }
}

func (p *P2p) redial(ctx context.Context, peerID peer.ID, knownPeersFunc func() []KnownPeer) {
	if p.IsConnected(peerID) || !isKnownPeer(knownPeersFunc(), peerID) {
		return
	}
	if _, pending := p.redials.LoadOrStore(peerID, struct{}{}); pending {
		return
	}
	defer p.redials.Delete(peerID)
	// The peer was connected a moment ago, whatever the schedule remembers about
	// it is out of date. The redial itself leaves no record there: it is a quick
	// try of the addresses we already have, and if it fails, the next round makes
	// a regular attempt instead of waiting out a pause.
	p.retries.forget(peerID)

	// Both sides see the connection go at the same moment and would dial each
	// other head-on every time.
	timer := time.NewTimer(rand.N(p.maxRedialDelay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	// The other side may have dialed first, or the peer was removed meanwhile
	if p.IsConnected(peerID) {
		awlmetrics.P2PRedialsTotal.WithLabelValues(redialPeerConnected).Inc()
		return
	}
	if !isKnownPeer(knownPeersFunc(), peerID) {
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, redialTimeout)
	defer cancel()
	start := time.Now()
	// Unlike ConnectPeer, no DHT lookup: we were connected a moment ago, the DHT
	// cannot know anything newer than the addresses still in the peerstore.
	// Not p.host either: the routed host would answer a failed dial with a
	// lookup. If these addresses do not work, the next round is to find out.
	err := p.basicHost.Connect(dialCtx, peer.AddrInfo{ID: peerID})
	took := time.Since(start)
	var result string
	switch {
	case err == nil:
		result = redialSuccess
		p.logger.Debugf("successfully redialed peer %s after disconnect in %s", peerID, took.Round(time.Millisecond))
	case ctx.Err() != nil:
		// it is us shutting down
		return
	case p.IsConnected(peerID):
		// not a failure to report: the peer has dialed us meanwhile
		result = redialPeerConnected
	default:
		result = redialFailure
		p.logger.Infof("redial to peer %s after disconnect failed in %s: %v", peerID, took.Round(time.Millisecond), err)
	}
	awlmetrics.P2PRedialsTotal.WithLabelValues(result).Inc()
	awlmetrics.P2PRedialDurationSeconds.WithLabelValues(result).Observe(took.Seconds())
}

func isKnownPeer(knownPeers []KnownPeer, peerID peer.ID) bool {
	return slices.ContainsFunc(knownPeers, func(knownPeer KnownPeer) bool {
		return knownPeer.ID == peerID
	})
}
