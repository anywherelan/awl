package p2p

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"
)

const (
	relayReserveTimeout = 10 * time.Second
	// relayRefreshBefore is how long before it expires a reservation is renewed
	relayRefreshBefore = 5 * time.Minute
)

// KnownPeersRelayService lets known peers use this host as a relay to reach
// each other: a peer behind NAT reserves a slot here, and the other known
// peers connect to it through this host. Other peers are refused.
//
// libp2p runs the relay only while the host is reachable from the internet.
func KnownPeersRelayService(isKnownPeer func(peer.ID) bool) libp2p.Option {
	return libp2p.EnableRelayService(
		relay.WithResources(knownPeersRelayResources()),
		relay.WithACL(knownPeersACL{isKnownPeer: isKnownPeer}),
	)
}

func knownPeersRelayResources() relay.Resources {
	rc := relay.DefaultResources()
	// The defaults are for a public relay that only helps to start a direct
	// connection: a relayed connection is reset after 2 minutes or 128 KiB. Here
	// it carries the VPN traffic between our own peers for as long as they
	// can't connect directly.
	rc.Limit = nil
	// The devices of one home share a public IP, limit only the total.
	rc.MaxReservationsPerIP = rc.MaxReservations
	rc.MaxReservationsPerASN = rc.MaxReservations

	return rc
}

// knownPeersACL allows reservations and relayed connections between known
// peers only.
type knownPeersACL struct {
	isKnownPeer func(peer.ID) bool
}

func (a knownPeersACL) AllowReserve(p peer.ID, _ multiaddr.Multiaddr) bool {
	return a.isKnownPeer(p)
}

func (a knownPeersACL) AllowConnect(src peer.ID, _ multiaddr.Multiaddr, dest peer.ID) bool {
	// dest was known when it made the reservation, it may have been removed since
	return a.isKnownPeer(src) && a.isKnownPeer(dest)
}

// knownRelays keeps a reservation at each known peer that relays for us, see
// KnownPeersRelayService, so that the other peers it knows can reach us through
// it when they can't connect to us directly. In turn, ConnectPeer and the
// redials try a peer through every relay where we hold a reservation: such a
// relay knows us, and it knows the peer if the peer reserved a slot there too.
//
// The rounds of MaintainBackgroundConnections make and renew the reservations,
// see maintain. The relay drops a reservation along with the connection it was
// made over, and so do we.
type knownRelays struct {
	p *P2p

	mu sync.Mutex
	// when our reservation expires, by relay
	reservations map[peer.ID]time.Time
	// relays with a reservation request going on
	reserving sync.Map
}

func newKnownRelays(p *P2p) *knownRelays {
	return &knownRelays{
		p:            p,
		reservations: make(map[peer.ID]time.Time),
	}
}

// start forgets a reservation when the relay is disconnected. The returned func
// stops it.
func (r *knownRelays) start() (stop func()) {
	sub, err := r.p.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		r.p.logger.Errorf("no relays among known peers: failed to subscribe to connectedness events: %v", err)
		return func() {}
	}

	go func() {
		// Reads until the subscription is closed: the event bus waits for every
		// subscriber to take the event.
		for e := range sub.Out() {
			evt := e.(event.EvtPeerConnectednessChanged)
			// the relay drops the reservations of a peer that has no
			// unlimited connection left
			if evt.Connectedness != network.Connected {
				r.forget(evt.Peer)
			}
		}
	}()

	return func() { _ = sub.Close() }
}

// maintain makes a reservation at a connected known peer that keeps us in its
// known peers and relays for them, or renews the one we have when it expires
// soon. The request is made in the background.
func (r *knownRelays) maintain(ctx context.Context, relayID peer.ID) {
	protos, err := r.p.host.Peerstore().SupportsProtocols(relayID, proto.ProtoIDv2Hop)
	if err != nil || len(protos) == 0 {
		// libp2p runs the relay only while the host is reachable from the internet
		r.forget(relayID)
		return
	}

	r.mu.Lock()
	expiresAt, ok := r.reservations[relayID]
	r.mu.Unlock()
	if ok && time.Until(expiresAt) > relayRefreshBefore {
		return
	}

	go r.reserve(ctx, relayID)
}

// reserve makes or renews our reservation at the relay.
func (r *knownRelays) reserve(ctx context.Context, relayID peer.ID) {
	if _, pending := r.reserving.LoadOrStore(relayID, struct{}{}); pending {
		return
	}
	defer r.reserving.Delete(relayID)
	// circuit relay v2 doesn't relay through another relay
	if r.p.directConnToPeer(relayID) == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, relayReserveTimeout)
	defer cancel()
	// Not p.host: the routed host would answer a failed dial with a DHT lookup.
	rsvp, err := client.Reserve(ctx, r.p.basicHost, peer.AddrInfo{ID: relayID})

	r.mu.Lock()
	defer r.mu.Unlock()
	expiresAt, had := r.reservations[relayID]
	if err != nil {
		// The relay refused: it doesn't know us anymore or it is full. A
		// request that failed otherwise is retried by the next round, while the
		// reservation we have is still valid.
		var rsvpErr client.ReservationError
		refused := errors.As(err, &rsvpErr) && rsvpErr.Status != pbv2.Status_CONNECTION_FAILED
		if refused || !time.Now().Before(expiresAt) {
			delete(r.reservations, relayID)
		}
		r.p.logger.Debugf("failed to reserve a slot at known peer %s: %v", relayID, err)
		return
	}
	// The connection may be gone while the request was on its way. Then the
	// relay has dropped the reservation already.
	if r.p.directConnToPeer(relayID) == nil {
		delete(r.reservations, relayID)
		return
	}
	r.reservations[relayID] = rsvp.Expiration
	if !had {
		r.p.logger.Infof("reserved a slot at known peer %s, other peers it knows can reach us through it", relayID)
	}
}

func (r *knownRelays) forget(relayID peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.reservations, relayID)
}

// circuitAddrs returns the addresses of the peer through the relays where we
// hold a reservation. They have no transport part: the relay is dialed over
// the connection we have to it.
func (r *knownRelays) circuitAddrs(peerID peer.ID) []multiaddr.Multiaddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var addrs []multiaddr.Multiaddr
	for relayID, expiresAt := range r.reservations {
		if relayID == peerID || !now.Before(expiresAt) {
			continue
		}
		relayAddr, err := multiaddr.NewMultiaddr("/p2p/" + relayID.String() + "/p2p-circuit")
		if err != nil {
			continue
		}
		addrs = append(addrs, relayAddr)
	}

	return addrs
}
