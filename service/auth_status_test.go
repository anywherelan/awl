package service

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/stretchr/testify/require"

	"github.com/anywherelan/awl/config"
)

// TestBackgroundExchangesSkipDisconnectedPeers pins that the periodic status
// and auth exchanges do not reach for a peer that is not connected: ConnectPeer
// looks it up in the DHT.
func TestBackgroundExchangesSkipDisconnectedPeers(t *testing.T) {
	online, offline := newTestPeerID(t), newTestPeerID(t)

	t.Setenv(config.AppDataDirEnvKey, t.TempDir())
	conf := config.NewConfig(config.AppTypeAwl, eventbus.NewBus())
	t.Cleanup(conf.Close)
	conf.Lock()
	// Neither has confirmed us, so there is an auth request to retry for both
	conf.KnownPeers[online.String()] = config.KnownPeer{PeerID: online.String()}
	conf.KnownPeers[offline.String()] = config.KnownPeer{PeerID: offline.String()}
	conf.Unlock()

	p2p := &recordingP2p{connected: online}
	auth := NewAuthStatus(p2p, conf, eventbus.NewBus())

	auth.ExchangeStatusInfoWithAllKnownPeers(t.Context())
	require.Equal(t, []peer.ID{online}, p2p.reached)

	p2p.reached = nil
	auth.retryAuthRequests(t.Context())
	require.Equal(t, []peer.ID{online}, p2p.reached)
}

func newTestPeerID(tb testing.TB) peer.ID {
	tb.Helper()
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(tb, err)
	peerID, err := peer.IDFromPrivateKey(key)
	require.NoError(tb, err)

	return peerID
}

// recordingP2p is a P2p without a network where one peer counts as connected.
// It records whom the service reached for and lets nothing through; anything
// else panics on the nil P2p.
type recordingP2p struct {
	P2p
	connected peer.ID
	reached   []peer.ID
}

func (*recordingP2p) SubscribeConnectionEvents(_, _ func(network.Network, network.Conn)) {}

func (p *recordingP2p) IsConnected(peerID peer.ID) bool {
	return peerID == p.connected
}

func (p *recordingP2p) ConnectPeer(_ context.Context, peerID peer.ID) error {
	p.reached = append(p.reached, peerID)
	return errors.New("no network")
}
