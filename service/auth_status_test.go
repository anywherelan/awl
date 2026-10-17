package service

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pProtocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/stretchr/testify/require"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/protocol"
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

// TestExchangeStreamsTimeOut: a peer that opens an auth or status stream and
// then goes silent does not hold it past exchangeStreamTimeout.
func TestExchangeStreamsTimeOut(t *testing.T) {
	oldTimeout := exchangeStreamTimeout
	exchangeStreamTimeout = 200 * time.Millisecond
	t.Cleanup(func() { exchangeStreamTimeout = oldTimeout })

	server, client := newTestHost(t), newTestHost(t)

	t.Setenv(config.AppDataDirEnvKey, t.TempDir())
	conf := config.NewConfig(config.AppTypeAwl, eventbus.NewBus())
	t.Cleanup(conf.Close)
	conf.Lock()
	// Known, so that the status handler reads from the stream too
	conf.KnownPeers[client.ID().String()] = config.KnownPeer{PeerID: client.ID().String()}
	conf.Unlock()

	auth := NewAuthStatus(&recordingP2p{}, conf, eventbus.NewBus())
	server.SetStreamHandler(protocol.AuthMethod, auth.AuthStreamHandler)
	server.SetStreamHandler(protocol.GetStatusMethod, auth.StatusStreamHandler)
	require.NoError(t, client.Connect(t.Context(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}))

	for _, proto := range []libp2pProtocol.ID{protocol.AuthMethod, protocol.GetStatusMethod} {
		t.Run(string(proto), func(t *testing.T) {
			stream, err := client.NewStream(t.Context(), server.ID(), proto)
			require.NoError(t, err)
			defer func() { _ = stream.Reset() }()
			// Only a guard against a hanging test, well above the timeout
			require.NoError(t, stream.SetReadDeadline(time.Now().Add(5*time.Second)))

			// Nothing is sent: the server gives up and closes the stream
			_, err = stream.Read(make([]byte, 1))
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func newTestHost(tb testing.TB) host.Host {
	tb.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = h.Close() })

	return h
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
