package p2p

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	awlmetrics "github.com/anywherelan/awl/metrics"
)

const (
	lanDialTimeout = 5 * time.Second
	// lanRetryDelay is how long the LAN addresses of a peer are not tried again
	// after a dial to them failed. Without it, a peer in another network with
	// the same subnet, such as 192.168.1.0/24, would be dialed every two
	// minutes: a relay cuts a relayed connection that often, and the new one
	// brings a new identify.
	lanRetryDelay = 10 * time.Minute

	// how a LAN dial ended, a metric label
	lanDialSuccess       = "success"
	lanDialFailure       = "failure"
	lanDialPeerConnected = "peer_connected"
)

// lanDialer opens a direct connection to a known peer that is connected only
// through a relay, at its addresses in our LAN.
//
// identify brings the addresses of a peer even over a relayed connection, but
// libp2p keeps the private ones only from a connection over a private address.
// So a peer in our LAN that was reached through a relay is never dialed at its
// LAN address otherwise.
type lanDialer struct {
	p           *P2p
	isKnownPeer func(peer.ID) bool
	// peers with a dial going on
	dialing sync.Map

	failedMu sync.Mutex
	// when a dial to these addresses failed, by lanDialKey
	failed map[string]time.Time
}

func newLANDialer(p *P2p, isKnownPeer func(peer.ID) bool) *lanDialer {
	return &lanDialer{
		p:           p,
		isKnownPeer: isKnownPeer,
		failed:      make(map[string]time.Time),
	}
}

// start dials known peers on identify. The returned func stops it.
func (l *lanDialer) start(ctx context.Context) (stop func()) {
	sub, err := l.p.host.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		l.p.logger.Errorf("no LAN dials: failed to subscribe to identify events: %v", err)
		return func() {}
	}

	go func() {
		// Reads until the subscription is closed: the event bus waits for every
		// subscriber to take the event, so the loop only picks out the events
		// worth a dial, and holds up the identify of other peers no longer.
		for e := range sub.Out() {
			evt := e.(event.EvtPeerIdentificationCompleted)
			if isRelayAddr(evt.Conn.RemoteMultiaddr()) && l.isKnownPeer(evt.Peer) {
				go l.dial(ctx, evt.Peer, evt.ListenAddrs)
			}
		}
	}()

	return func() { _ = sub.Close() }
}

// dial tries the LAN addresses among peerAddrs, the addresses the peer
// announced in identify.
func (l *lanDialer) dial(ctx context.Context, peerID peer.ID, peerAddrs []multiaddr.Multiaddr) {
	// Not only a LAN connection: a direct one over a public address, IPv6 or
	// the router (hairpin) is as good.
	if l.p.directConnToPeer(peerID) != nil {
		return
	}
	// TODO: think about a limit on the number of candidates. A known peer
	// chooses which hosts and ports of our LAN we dial: every private address
	// in our subnets that it announces, and each new set of addresses in an
	// identify push is dialed again. Only known peers can do this, so the risk
	// is accepted for now.
	addrs := l.p.localAddrs.lanCandidates(peerAddrs)
	if len(addrs) == 0 {
		return
	}
	key := lanDialKey(peerID, addrs)
	if l.failedRecently(key) {
		return
	}
	if _, pending := l.dialing.LoadOrStore(peerID, struct{}{}); pending {
		return
	}
	defer l.dialing.Delete(peerID)

	// Both sides may dial each other at once. That leaves two direct
	// connections, which does no harm.
	dialCtx, cancel := context.WithTimeout(network.WithForceDirectDial(ctx, "lan"), lanDialTimeout)
	defer cancel()
	start := time.Now()
	// Not p.host: the routed host would answer a failed dial with a DHT lookup.
	// With force direct, basicHost dials even though a relayed connection is
	// there, over these and the other direct addresses in the peerstore.
	err := l.p.basicHost.Connect(dialCtx, peer.AddrInfo{ID: peerID, Addrs: addrs})
	took := time.Since(start).Round(time.Millisecond)
	conn := l.p.directConnToPeer(peerID)
	var result string
	switch {
	case err == nil && conn != nil:
		result = lanDialSuccess
		l.p.logger.Infof("connected to peer %s directly over %s in %s, it was connected only through a relay", peerID, conn.RemoteMultiaddr(), took)
	case ctx.Err() != nil:
		// it is us shutting down
		return
	case conn != nil:
		// not a failure to report: the peer has dialed us meanwhile
		result = lanDialPeerConnected
	default:
		result = lanDialFailure
		l.rememberFailure(key)
		l.p.logger.Debugf("failed to connect to peer %s over LAN addresses %v in %s: %v", peerID, addrs, took, err)
	}
	awlmetrics.P2PLANDialsTotal.WithLabelValues(result).Inc()
}

func (l *lanDialer) failedRecently(key string) bool {
	l.failedMu.Lock()
	defer l.failedMu.Unlock()
	failedAt, ok := l.failed[key]

	return ok && time.Since(failedAt) < lanRetryDelay
}

func (l *lanDialer) rememberFailure(key string) {
	l.failedMu.Lock()
	defer l.failedMu.Unlock()
	now := time.Now()
	// what is no longer held against anyone, so that the map does not grow
	maps.DeleteFunc(l.failed, func(_ string, failedAt time.Time) bool {
		return now.Sub(failedAt) >= lanRetryDelay
	})
	l.failed[key] = now
}

// forgetFailures lets every peer be dialed again: after the network changed,
// addresses that failed may work.
func (l *lanDialer) forgetFailures() {
	l.failedMu.Lock()
	defer l.failedMu.Unlock()
	clear(l.failed)
}

// lanDialKey stands for a dial to the peer at these addresses, in any order.
func lanDialKey(peerID peer.ID, addrs []multiaddr.Multiaddr) string {
	strs := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		strs = append(strs, addr.String())
	}
	slices.Sort(strs)

	return peerID.String() + " " + strings.Join(strs, " ")
}
