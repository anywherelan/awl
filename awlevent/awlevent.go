package awlevent

import (
	"context"

	"github.com/libp2p/go-libp2p/core/event"

	"github.com/anywherelan/awl/protocol"
)

type Bus = event.Bus
type Emitter = event.Emitter

type KnownPeerChanged struct {
}

type ReceivedAuthRequest struct {
	protocol.AuthPeer
	PeerID string
}

// InviteRedeemed is emitted when a peer joins through one of our invite links
// (config.Invite), carrying the non-secret invite ID.
type InviteRedeemed struct {
	PeerID   string
	InviteID string
}

// VPNGatewayConnectivityChanged is emitted (client mode only) when the
// connection to the configured VPN gateway peer goes up or down, so the UI /
// tray can reflect gateway reachability immediately instead of waiting for the
// next GatewayInfo poll. GatewayInfo.Connected stays the canonical state; this
// event is a low-latency edge signal, deduplicated to fire only on transitions.
type VPNGatewayConnectivityChanged struct {
	Connected bool
	PeerID    string
}

func WrapSubscriptionToCallback(ctx context.Context, callback func(interface{}), bus Bus,
	eventType interface{}, opts ...event.SubscriptionOpt) {
	sub, err := bus.Subscribe(eventType, opts...)
	if err != nil {
		panic(err)
	}

	go func() {
		defer sub.Close()

		for {
			select {
			case ev, ok := <-sub.Out():
				if !ok {
					return
				}
				callback(ev)
			case <-ctx.Done():
				return
			}
		}
	}()
}
