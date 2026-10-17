package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pProtocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/stretchr/testify/require"

	"github.com/anywherelan/awl/awlevent"
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

// TestIngoingAuthsQueue: requests waiting for a manual accept are limited to
// maxIngoingAuths. Over the limit a new request takes the place of a stale one
// or is not kept, and a repeated request only refreshes its entry. An event is
// emitted for a new entry only.
func TestIngoingAuthsQueue(t *testing.T) {
	t.Run("over the limit a request is not kept", func(t *testing.T) {
		q := newAuthQueueTest(t)
		clients := q.newClients(t, maxIngoingAuths+10)

		for i, client := range clients[:maxIngoingAuths] {
			q.sendAuth(t, client, fmt.Sprintf("peer %d", i))
		}
		require.Equal(t, maxIngoingAuths, q.drainEvents())

		for i, client := range clients[maxIngoingAuths:] {
			response := q.sendAuth(t, client, fmt.Sprintf("extra %d", i))
			// Answered as pending, so that the sender repeats it later
			require.Equal(t, protocol.AuthPeerResponse{}, response)
		}
		require.Zero(t, q.drainEvents())
		q.requireQueued(t, clients[:maxIngoingAuths]...)
	})

	t.Run("a repeated request refreshes its entry", func(t *testing.T) {
		q := newAuthQueueTest(t)
		client := q.newClients(t, 1)[0]

		q.sendAuth(t, client, "old name")
		q.sendAuth(t, client, "new name")

		require.Equal(t, 1, q.drainEvents())
		requests := q.auth.GetIngoingAuthRequests()
		require.Len(t, requests, 1)
		require.Equal(t, "new name", requests[client.ID().String()].Name)
	})

	t.Run("a stale request is dropped for a new one", func(t *testing.T) {
		q := newAuthQueueTest(t)
		clients := q.newClients(t, maxIngoingAuths+2)
		queued, newcomer, late := clients[:maxIngoingAuths], clients[maxIngoingAuths], clients[maxIngoingAuths+1]

		for i, client := range queued {
			q.sendAuth(t, client, fmt.Sprintf("peer %d", i))
		}
		// All but the first sender repeat their requests in time
		q.advance(staleIngoingAuthAge - time.Minute)
		for i, client := range queued[1:] {
			q.sendAuth(t, client, fmt.Sprintf("peer %d", i+1))
		}
		q.advance(2 * time.Minute)
		require.Equal(t, maxIngoingAuths, q.drainEvents())

		q.sendAuth(t, newcomer, "newcomer")
		require.Equal(t, 1, q.drainEvents())
		q.requireQueued(t, append([]host.Host{newcomer}, queued[1:]...)...)

		// No stale request is left to drop
		q.sendAuth(t, late, "late")
		require.Zero(t, q.drainEvents())
		q.requireQueued(t, append([]host.Host{newcomer}, queued[1:]...)...)
	})
}

type authQueueTest struct {
	server  host.Host
	auth    *AuthStatus
	events  event.Subscription
	elapsed atomic.Int64
}

func newAuthQueueTest(t *testing.T) *authQueueTest {
	t.Setenv(config.AppDataDirEnvKey, t.TempDir())
	conf := config.NewConfig(config.AppTypeAwl, eventbus.NewBus())
	t.Cleanup(conf.Close)

	bus := eventbus.NewBus()
	events, err := bus.Subscribe(new(awlevent.ReceivedAuthRequest), eventbus.BufSize(2*maxIngoingAuths))
	require.NoError(t, err)
	t.Cleanup(func() { _ = events.Close() })

	q := &authQueueTest{server: newTestHost(t), events: events}
	q.auth = NewAuthStatus(&recordingP2p{}, conf, bus)
	start := time.Now()
	q.auth.now = func() time.Time { return start.Add(time.Duration(q.elapsed.Load())) }
	q.server.SetStreamHandler(protocol.AuthMethod, q.auth.AuthStreamHandler)

	return q
}

func (q *authQueueTest) newClients(t *testing.T, n int) []host.Host {
	clients := make([]host.Host, n)
	for i := range clients {
		clients[i] = newTestHost(t)
		require.NoError(t, clients[i].Connect(t.Context(), peer.AddrInfo{ID: q.server.ID(), Addrs: q.server.Addrs()}))
	}
	return clients
}

func (q *authQueueTest) sendAuth(t *testing.T, client host.Host, name string) protocol.AuthPeerResponse {
	stream, err := client.NewStream(t.Context(), q.server.ID(), protocol.AuthMethod)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	require.NoError(t, protocol.SendAuth(stream, protocol.AuthPeer{Name: name}))
	response, err := protocol.ReceiveAuthResponse(stream)
	require.NoError(t, err)

	return response
}

func (q *authQueueTest) advance(d time.Duration) {
	q.elapsed.Add(int64(d))
}

// drainEvents returns the number of ReceivedAuthRequest events so far. The
// handler emits before it answers, so they are all in by now.
func (q *authQueueTest) drainEvents() int {
	for n := 0; ; n++ {
		select {
		case <-q.events.Out():
		default:
			return n
		}
	}
}

func (q *authQueueTest) requireQueued(t *testing.T, clients ...host.Host) {
	t.Helper()
	want := make([]string, 0, len(clients))
	for _, client := range clients {
		want = append(want, client.ID().String())
	}
	require.ElementsMatch(t, want, slices.Collect(maps.Keys(q.auth.GetIngoingAuthRequests())))
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
