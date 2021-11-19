package awl

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/vpn"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-peerstore/pstoremem"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	"golang.zx2c4.com/wireguard/tun"
)

func init() {
	useAwldns = false
}

func TestMakeFriends(t *testing.T) {
	a := require.New(t)
	closeBootstrapNode := initBootstrapNode(t)
	defer closeBootstrapNode()

	peer1 := newTestPeer(t, false)
	defer peer1.Close()
	peer2 := newTestPeer(t, false)
	defer peer2.Close()

	makeFriends(a, peer2, peer1)
}

func TestRemovePeer(t *testing.T) {
	a := require.New(t)
	closeBootstrapNode := initBootstrapNode(t)
	defer closeBootstrapNode()

	peer1 := newTestPeer(t, false)
	defer peer1.Close()
	peer2 := newTestPeer(t, false)
	defer peer2.Close()

	makeFriends(a, peer2, peer1)

	// Remove peer2 from peer1
	err := peer1.api.RemovePeer(peer2.PeerID())
	a.NoError(err)

	peer2From1, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	a.EqualError(err, "peer not found")
	a.Nil(peer2From1)
	_, blockedPeerExists := peer1.app.Conf.GetBlockedPeer(peer2.PeerID())
	a.True(blockedPeerExists)

	time.Sleep(500 * time.Millisecond)
	peer1From2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	a.NoError(err)
	a.NotNil(peer1From2)
	a.True(peer1From2.Confirmed)
	a.True(peer1From2.Declined)

	a.Len(peer1.app.AuthStatus.GetIngoingAuthRequests(), 0)
	a.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)

	// Add peer2 from peer1 - should succeed
	err = peer1.api.SendFriendRequest(peer2.PeerID(), "")
	a.NoError(err)
	time.Sleep(500 * time.Millisecond)

	peer2From1, err = peer1.api.KnownPeerConfig(peer2.PeerID())
	a.NoError(err)
	a.True(peer2From1.Confirmed)
	a.False(peer2From1.Declined)

	_, blockedPeerExists = peer1.app.Conf.GetBlockedPeer(peer2.PeerID())
	a.False(blockedPeerExists)

	peer1From2, err = peer2.api.KnownPeerConfig(peer1.PeerID())
	a.NoError(err)
	a.NotNil(peer1From2)
	a.True(peer1From2.Confirmed)
	a.False(peer1From2.Declined)

	a.Len(peer1.app.AuthStatus.GetIngoingAuthRequests(), 0)
	a.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
}

func TestDeclinePeerFriendRequest(t *testing.T) {
	a := require.New(t)
	closeBootstrapNode := initBootstrapNode(t)
	defer closeBootstrapNode()

	peer1 := newTestPeer(t, false)
	defer peer1.Close()
	peer2 := newTestPeer(t, false)
	defer peer2.Close()
	ensurePeersAvailableInDHT(a, peer1, peer2)

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "")
	a.NoError(err)

	var authRequests []entity.AuthRequest
	a.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		a.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.ReplyFriendRequest(authRequests[0].PeerID, "", true)
	a.NoError(err)

	time.Sleep(500 * time.Millisecond)
	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	a.True(exists)
	a.False(knownPeer.Confirmed)
	a.True(knownPeer.Declined)

	a.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
	_, blockedPeerExists := peer2.app.Conf.GetBlockedPeer(peer1.PeerID())
	a.True(blockedPeerExists)
}

func BenchmarkTunnelPackets(b *testing.B) {
	a := require.New(b)
	closeBootstrapNode := initBootstrapNode(b)
	defer closeBootstrapNode()

	peer1 := newTestPeer(b, true)
	defer peer1.Close()
	peer2 := newTestPeer(b, true)
	defer peer2.Close()

	makeFriends(a, peer2, peer1)
	b.ResetTimer()

	packetSizes := []int{40, 300, 800, 1300, 1800, 2300, 2800, 3500}
	for _, packetSize := range packetSizes {
		b.Run(fmt.Sprintf("%d bytes per package", packetSize), func(b *testing.B) {
			b.SetBytes(int64(packetSize))
			var packetsSent int64
			packet := testPacket(packetSize)
			peer2.tun.ReferenceInboundPacketLen = len(packet)
			peer2.tun.ClearInboundCount()
			for i := 0; i < b.N; i++ {
				peer1.tun.Outbound <- packet
				atomic.AddInt64(&packetsSent, 1)
				// to have packet_loss at reasonable level (but more than 0)
				const sleepEvery = 40
				if i != 0 && i%sleepEvery == 0 {
					time.Sleep(sleepEvery * 8 * time.Microsecond)
				}
			}
			received := peer2.tun.InboundCount()
			sent := atomic.LoadInt64(&packetsSent)
			packetLoss := (float64(1) - float64(received)/float64(sent)) * 100
			bandwidth := float64(received) * float64(packetSize) / 1024 / 1024
			b.ReportMetric(bandwidth, "MB/s")
			b.ReportMetric(float64(received), "packets/s")
			b.ReportMetric(packetLoss, "packet_loss")
		})
	}
}

func testPacket(length int) []byte {
	data, err := hex.DecodeString("4500002828f540004011fd490a4200010a420002a9d0238200148bfd68656c6c6f20776f726c6421")
	if err != nil {
		panic(err)
	}

	packet := data
	if length > len(data) {
		packet = make([]byte, length)
		copy(packet, data)
		rand.Read(packet[len(data):])
	}

	vpnPacket := vpn.Packet{}
	_, err = vpnPacket.ReadFrom(bytes.NewReader(packet))
	if err != nil {
		panic(err)
	}
	vpnPacket.Parse()
	vpnPacket.RecalculateChecksum()

	return vpnPacket.Packet
}

type testPeer struct {
	app *Application
	api *apiclient.Client
	tun *TestTUN
}

func (tp testPeer) Close() {
	tp.app.Close()
}

func (tp testPeer) PeerID() string {
	return tp.app.Conf.P2pNode.PeerID
}

func newTestPeer(t testing.TB, disableLogging bool) testPeer {
	a := require.New(t)

	tempDir := t.TempDir()
	t.Setenv(config.AppDataDirEnvKey, tempDir)
	if disableLogging {
		tempConf := config.NewConfig(eventbus.NewBus())
		tempConf.LoggerLevel = "fatal"
		tempConf.Save()
	}

	app := New()
	app.SetupLoggerAndConfig()
	if disableLogging {
		log.SetupLogging(zapcore.NewNopCore(), func(string) zapcore.Level {
			return zapcore.FatalLevel
		})
	}
	app.Conf.HttpListenAddress = "127.0.0.1:0"
	app.Conf.SetListenAddresses(p2p.UnicastListenAddrs())

	testTUN := NewTestTUN()
	err := app.Init(context.Background(), testTUN.TUN())
	a.NoError(err)

	return testPeer{
		app: app,
		api: apiclient.New(app.Api.Address()),
		tun: testTUN,
	}
}

func initBootstrapNode(t testing.TB) func() {
	hostConfig := p2p.HostConfig{
		PrivKeyBytes: nil,
		ListenAddrs: []multiaddr.Multiaddr{
			multiaddr.StringCast("/ip4/127.0.0.1/tcp/0"),
			multiaddr.StringCast("/ip4/127.0.0.1/udp/0/quic"),
		},
		UserAgent:      config.UserAgent,
		BootstrapPeers: []peer.AddrInfo{},
		Libp2pOpts: []libp2p.Option{
			libp2p.DisableRelay(),
			libp2p.ForceReachabilityPublic(),
		},
		Peerstore:    pstoremem.NewPeerstore(),
		DHTDatastore: dssync.MutexWrap(ds.NewMapDatastore()),
	}

	p2pSrv := p2p.NewP2p(context.Background())
	p2pHost, err := p2pSrv.InitHost(hostConfig)
	require.NoError(t, err)
	err = p2pSrv.Bootstrap()
	require.NoError(t, err)

	peerInfo := peer.AddrInfo{ID: p2pHost.ID(), Addrs: p2pHost.Addrs()}
	addrs, err := peer.AddrInfoToP2pAddrs(&peerInfo)
	require.NoError(t, err)

	previousBootstrapPeers := config.DefaultBootstrapPeers
	config.DefaultBootstrapPeers = addrs

	return func() {
		config.DefaultBootstrapPeers = previousBootstrapPeers
		_ = p2pSrv.Close()
	}
}

func ensurePeersAvailableInDHT(a *require.Assertions, peer1, peer2 testPeer) {
	a.Eventually(func() bool {
		_, err1 := peer1.app.P2p.FindPeer(context.Background(), peer2.app.P2p.PeerID())
		_, err2 := peer2.app.P2p.FindPeer(context.Background(), peer1.app.P2p.PeerID())

		return err1 == nil && err2 == nil
	}, time.Second, 30*time.Millisecond)
}

func makeFriends(a *require.Assertions, peer1, peer2 testPeer) {
	ensurePeersAvailableInDHT(a, peer1, peer2)
	err := peer1.api.SendFriendRequest(peer2.PeerID(), "")
	a.NoError(err)

	var authRequests []entity.AuthRequest
	a.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		a.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.ReplyFriendRequest(authRequests[0].PeerID, "", false)
	a.NoError(err)

	time.Sleep(500 * time.Millisecond)
	a.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
	knownPeer, exists := peer2.app.Conf.GetPeer(peer1.PeerID())
	a.True(exists)
	a.True(knownPeer.Confirmed)

	knownPeer, exists = peer1.app.Conf.GetPeer(peer2.PeerID())
	a.True(exists)
	a.True(knownPeer.Confirmed)
}

type TestTUN struct {
	Outbound                  chan []byte
	ReferenceInboundPacketLen int

	inboundCount int64
	closed       chan struct{}
	events       chan tun.Event
	tun          testTun
}

func NewTestTUN() *TestTUN {
	c := &TestTUN{
		Outbound: make(chan []byte),
		closed:   make(chan struct{}),
		events:   make(chan tun.Event, 1),
	}
	c.tun.t = c
	c.events <- tun.EventUp
	return c
}

func (c *TestTUN) TUN() tun.Device {
	return &c.tun
}

func (c *TestTUN) InboundCount() int64 {
	return atomic.LoadInt64(&c.inboundCount)
}

func (c *TestTUN) ClearInboundCount() {
	atomic.StoreInt64(&c.inboundCount, 0)
}

type testTun struct {
	t *TestTUN
}

func (t *testTun) File() *os.File { return nil }

func (t *testTun) Read(data []byte, offset int) (int, error) {
	select {
	case <-t.t.closed:
		return 0, os.ErrClosed
	case msg := <-t.t.Outbound:
		return copy(data[offset:], msg), nil
	}
}

func (t *testTun) Write(data []byte, offset int) (int, error) {
	msg := data[offset:]
	if len(msg) != t.t.ReferenceInboundPacketLen {
		return 0, errors.New("packets length mismatch")
	}
	select {
	case <-t.t.closed:
		return 0, os.ErrClosed
	default:
	}
	atomic.AddInt64(&t.t.inboundCount, 1)

	return len(msg), nil
}

func (t *testTun) Flush() error           { return nil }
func (t *testTun) MTU() (int, error)      { return vpn.InterfaceMTU, nil }
func (t *testTun) Name() (string, error)  { return "testTun", nil }
func (t *testTun) Events() chan tun.Event { return t.t.events }
func (t *testTun) Close() error {
	close(t.t.closed)
	close(t.t.events)
	return nil
}
