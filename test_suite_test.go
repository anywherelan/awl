package awl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/vpn"
)

func init() {
	// TODO: move to config
	useAwldns = false
	config.DefaultBootstrapPeers = nil
}

// TODO: add support for goleak in TestSuite
type TestSuite struct {
	*require.Assertions

	t                 testing.TB
	isSimnet          bool
	bootstrapAddrs    []peer.AddrInfo
	bootstrapAddrsStr []string
}

func NewTestSuite(t testing.TB) *TestSuite {
	// TODO: fix
	if os.Getenv("CI") != "" && runtime.GOOS == "linux" {
		t.Skip("doesn't work on linux in CI, flaky ensurePeersAvailableInDHT can't find peers")
	}

	ts := &TestSuite{t: t, Assertions: require.New(t)}
	ts.initBootstrapNode()
	ts.initBootstrapNode()

	return ts
}

func NewSimnetTestSuite(t testing.TB) *TestSuite {
	ts := &TestSuite{t: t, Assertions: require.New(t), isSimnet: true}

	return ts
}

type TestPeer struct {
	app *Application
	api *apiclient.Client
	tun *TestTUN
}

func (tp TestPeer) Close() {
	tp.app.Close()
}

func (tp TestPeer) PeerID() string {
	return tp.app.Conf.P2pNode.PeerID
}

func (ts *TestSuite) NewTestPeer(disableLogging bool) TestPeer {
	listenAddrs := []multiaddr.Multiaddr{
		multiaddr.StringCast("/ip4/127.0.0.1/tcp/0"),
		multiaddr.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"),
	}
	return ts.newTestPeer(disableLogging, listenAddrs, nil)
}

func (ts *TestSuite) newTestPeer(disableLogging bool, listenAddrs []multiaddr.Multiaddr, extraLibp2pOpts []libp2p.Option) TestPeer {
	tempDir := ts.t.TempDir()
	ts.t.Setenv(config.AppDataDirEnvKey, tempDir)
	tempConf := config.NewConfig(eventbus.NewBus())
	if disableLogging {
		tempConf.LoggerLevel = "fatal"
		log.SetAllLoggers(log.LevelFatal)
	}
	tempConf.Save()

	app := New()
	app.AllowEmptyBootstrapPeers = ts.isSimnet
	app.ExtraLibp2pOpts = extraLibp2pOpts

	app.SetupLoggerAndConfig()
	if disableLogging {
		log.SetupLogging(zapcore.NewNopCore(), func(string) zapcore.Level {
			return zapcore.FatalLevel
		})
	}
	app.Conf.HttpListenAddress = "127.0.0.1:0"
	app.Conf.HttpListenOnAdminHost = false
	app.Conf.SetListenAddresses(listenAddrs)
	app.Conf.P2pNode.BootstrapPeers = ts.bootstrapAddrsStr
	if ts.isSimnet {
		app.Conf.SOCKS5 = config.SOCKS5Config{
			ListenerEnabled: false,
			ProxyingEnabled: false,
		}
	} else {
		app.Conf.SOCKS5 = config.SOCKS5Config{
			ListenerEnabled: true,
			ProxyingEnabled: true,
			ListenAddress:   pickFreeAddr(ts.t),
			UsingPeerID:     "",
		}
	}

	testTUN := NewTestTUN()
	err := app.Init(context.Background(), testTUN.TUN())
	ts.NoError(err)

	tp := TestPeer{
		app: app,
		api: apiclient.New(app.Api.Address()),
		tun: testTUN,
	}

	ts.t.Cleanup(func() {
		tp.Close()
	})

	return tp
}

// TODO: rewrite bootstrap node using newTestPeer
func (ts *TestSuite) initBootstrapNode() {
	peerstore, err := pstoremem.NewPeerstore()
	ts.NoError(err)
	resourceLimitsConfig := rcmgr.InfiniteLimits
	mgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(resourceLimitsConfig))
	ts.NoError(err)

	hostConfig := p2p.HostConfig{
		PrivKeyBytes: nil,
		ListenAddrs: []multiaddr.Multiaddr{
			multiaddr.StringCast("/ip4/127.0.0.1/tcp/0"),
			multiaddr.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"),
		},
		UserAgent:                config.UserAgent,
		BootstrapPeers:           ts.bootstrapAddrs,
		AllowEmptyBootstrapPeers: true,
		Libp2pOpts: []libp2p.Option{
			libp2p.DisableRelay(),
			libp2p.ForceReachabilityPublic(),
			libp2p.ResourceManager(mgr),
		},
		Peerstore:    peerstore,
		DHTDatastore: dssync.MutexWrap(ds.NewMapDatastore()),
		DHTOpts: []dht.Option{
			dht.Mode(dht.ModeServer),
		},
	}

	p2pSrv := p2p.NewP2p(context.Background())
	p2pHost, err := p2pSrv.InitHost(hostConfig)
	ts.NoError(err)
	err = p2pSrv.Bootstrap()
	ts.NoError(err)

	peerInfo := peer.AddrInfo{ID: p2pHost.ID(), Addrs: p2pHost.Addrs()}
	ts.bootstrapAddrs = append(ts.bootstrapAddrs, peerInfo)
	addrs, err := peer.AddrInfoToP2pAddrs(&peerInfo)
	ts.NoError(err)
	for _, addr := range addrs {
		ts.bootstrapAddrsStr = append(ts.bootstrapAddrsStr, addr.String())
	}

	ts.t.Cleanup(func() {
		_ = p2pSrv.Close()
	})
}

func (ts *TestSuite) ensurePeersAvailableInDHT(peer1, peer2 TestPeer) {
	ts.Eventually(func() bool {
		err1 := peer1.app.P2p.Bootstrap()
		err2 := peer2.app.P2p.Bootstrap()
		if err1 != nil || err2 != nil {
			return false
		}

		_, err1 = peer1.app.P2p.FindPeer(context.Background(), peer2.app.P2p.PeerID())
		_, err2 = peer2.app.P2p.FindPeer(context.Background(), peer1.app.P2p.PeerID())

		return err1 == nil && err2 == nil
	}, 20*time.Second, 100*time.Millisecond)
}

func (ts *TestSuite) makeFriends(peer1, peer2 TestPeer) {
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.sendAndAcceptFriendRequest(peer1, peer2)
}

func (ts *TestSuite) makeFriendsSimnet(peer1, peer2 TestPeer) {
	// Direct connection in simlibp2p/simnet
	// Can't use DHT here because we don't have bootstrap peers

	p2Info := peer.AddrInfo{
		ID:    peer2.app.P2p.PeerID(),
		Addrs: peer2.app.P2p.Host().Addrs(),
	}
	ctx, cancel := context.WithTimeout(peer1.app.Ctx(), 5*time.Second)
	defer cancel()
	err := peer1.app.P2p.Host().Connect(ctx, p2Info)
	ts.NoError(err)

	ts.sendAndAcceptFriendRequest(peer1, peer2)
}

func (ts *TestSuite) sendAndAcceptFriendRequest(peer1, peer2 TestPeer) {
	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
	ts.NoError(err)

	var authRequests []entity.AuthRequest
	ts.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		ts.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.ReplyFriendRequest(authRequests[0].PeerID, "peer_1", false)
	ts.NoError(err)

	time.Sleep(500 * time.Millisecond)
	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
	knownPeer, exists := peer2.app.Conf.GetPeer(peer1.PeerID())
	ts.True(exists)
	ts.True(knownPeer.Confirmed)

	knownPeer, exists = peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.True(knownPeer.Confirmed)
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

func (t *testTun) Read(bufs [][]byte, sizes []int, offset int) (n int, err error) {
	for i, buf := range bufs {
		select {
		case <-t.t.closed:
			return n, os.ErrClosed
		case msg := <-t.t.Outbound:
			copyN := copy(buf[offset:], msg)
			sizes[i] = copyN
			n++
		}
	}

	return n, nil
}

func (t *testTun) Write(bufs [][]byte, offset int) (n int, err error) {
	for _, buf := range bufs {
		msg := buf[offset:]
		if len(msg) != t.t.ReferenceInboundPacketLen {
			return n, errors.New("packets length mismatch")
		}
		select {
		case <-t.t.closed:
			return n, os.ErrClosed
		default:
		}
		atomic.AddInt64(&t.t.inboundCount, 1)
		n++
	}

	return n, nil
}

func (t *testTun) BatchSize() int {
	return 1
}

func (t *testTun) Flush() error             { return nil }
func (t *testTun) MTU() (int, error)        { return vpn.InterfaceMTU, nil }
func (t *testTun) Name() (string, error)    { return "testTun", nil }
func (t *testTun) Events() <-chan tun.Event { return t.t.events }
func (t *testTun) Close() error {
	close(t.t.closed)
	close(t.t.events)
	return nil
}

func pickFreeAddr(t testing.TB) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	return l.Addr().String()
}

func testPacket(length int) []byte {
	return testPacketWithDest(length, "10.66.0.2")
}

func testPacketWithDest(length int, destIP string) []byte {
	data, err := hex.DecodeString("4500002828f540004011fd490a4200010a420002a9d0238200148bfd68656c6c6f20776f726c6421")
	if err != nil {
		panic(err)
	}

	packet := data
	if length > len(data) {
		packet = make([]byte, length)
		copy(packet, data)
		_, err = rand.Read(packet[len(data):])
		if err != nil {
			panic(err)
		}
	}

	vpnPacket := vpn.Packet{}
	_, err = vpnPacket.ReadFrom(bytes.NewReader(packet))
	if err != nil {
		panic(err)
	}
	vpnPacket.Parse()

	destIPParsed := net.ParseIP(destIP).To4()
	if destIPParsed == nil {
		panic(fmt.Sprintf("invalid destination IP: %s", destIP))
	}
	copy(vpnPacket.Dst, destIPParsed)

	vpnPacket.RecalculateChecksum()

	return vpnPacket.Packet
}
