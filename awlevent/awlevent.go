package awlevent

import (
	"context"

	"github.com/anywherelan/awl/protocol"
	"github.com/libp2p/go-libp2p/core/event"
)

type Bus = event.Bus
type Emitter = event.Emitter

type KnownPeerChanged struct {
}

type ReceivedAuthRequest struct {
	protocol.AuthPeer
	PeerID string
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
