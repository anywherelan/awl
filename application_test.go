package awl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
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
	"github.com/quic-go/quic-go/integrationtests/tools/israce"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/zap/zapcore"
	"golang.org/x/net/proxy"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/vpn"
)

func init() {
	useAwldns = false
	config.DefaultBootstrapPeers = nil
}

func TestMakeFriends(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)

	ts.makeFriends(peer2, peer1)
}

func TestRemovePeer(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)

	ts.makeFriends(peer2, peer1)

	// Remove peer2 from peer1
	err := peer1.api.RemovePeer(peer2.PeerID())
	ts.NoError(err)

	peer2From1, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	ts.EqualError(err, "peer not found")
	ts.Nil(peer2From1)
	_, blockedPeerExists := peer1.app.Conf.GetBlockedPeer(peer2.PeerID())
	ts.True(blockedPeerExists)

	time.Sleep(500 * time.Millisecond)
	peer1From2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)
	ts.NotNil(peer1From2)
	ts.True(peer1From2.Confirmed)
	ts.True(peer1From2.Declined)

	ts.Len(peer1.app.AuthStatus.GetIngoingAuthRequests(), 0)
	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)

	// Add peer2 from peer1 - should succeed
	err = peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
	ts.NoError(err)
	time.Sleep(500 * time.Millisecond)

	peer2From1, err = peer1.api.KnownPeerConfig(peer2.PeerID())
	ts.NoError(err)
	ts.True(peer2From1.Confirmed)
	ts.False(peer2From1.Declined)

	_, blockedPeerExists = peer1.app.Conf.GetBlockedPeer(peer2.PeerID())
	ts.False(blockedPeerExists)

	peer1From2, err = peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)
	ts.NotNil(peer1From2)
	ts.True(peer1From2.Confirmed)
	ts.False(peer1From2.Declined)

	ts.Len(peer1.app.AuthStatus.GetIngoingAuthRequests(), 0)
	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
}

func TestDeclinePeerFriendRequest(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
	ts.NoError(err)

	var authRequests []entity.AuthRequest
	ts.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		ts.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.ReplyFriendRequest(authRequests[0].PeerID, "peer_1", true)
	ts.NoError(err)

	time.Sleep(500 * time.Millisecond)
	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.False(knownPeer.Confirmed)
	ts.True(knownPeer.Declined)

	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
	_, blockedPeerExists := peer2.app.Conf.GetBlockedPeer(peer1.PeerID())
	ts.True(blockedPeerExists)
}

func TestAutoAcceptFriendRequest(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	peer2.app.Conf.Lock()
	peer2.app.Conf.P2pNode.AutoAcceptAuthRequests = true
	peer2.app.Conf.Unlock()

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
	ts.NoError(err)

	ts.Eventually(func() bool {
		knownPeers, err := peer2.api.KnownPeers()
		ts.NoError(err)
		return len(knownPeers) == 1
	}, 15*time.Second, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.True(knownPeer.Confirmed)
	ts.False(knownPeer.Declined)

	knownPeer, exists = peer2.app.Conf.GetPeer(peer1.PeerID())
	ts.True(exists)
	ts.True(knownPeer.Confirmed)
	ts.False(knownPeer.Declined)
}

func TestUniquePeerAlias(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)
	peer3 := ts.newTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.ensurePeersAvailableInDHT(peer2, peer3)

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer")
	ts.NoError(err)

	time.Sleep(200 * time.Millisecond)

	err = peer1.api.SendFriendRequest(peer3.PeerID(), "peer")
	ts.EqualError(err, api.ErrorPeerAliasIsNotUniq)
}

func TestUpdateUseAsExitNodeConfig(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)

	ts.makeFriends(peer2, peer1)

	current := goleak.IgnoreCurrent()
	goleak.VerifyNone(t, current)

	info, err := peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal("", info.SOCKS5.UsingPeerID)

	availableProxies, err := peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 0)

	peer1Config, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)
	ts.Equal(false, peer1Config.AllowedUsingAsExitNode)

	// allow, check that peer1 got our config
	err = peer2.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer1.PeerID(),
		Alias:                peer1Config.Alias,
		DomainName:           peer1Config.DomainName,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	var peer2Config *config.KnownPeer
	ts.Eventually(func() bool {
		peer2Config, err = peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)

		return peer2Config.AllowedUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	info, err = peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal(peer2.PeerID(), info.SOCKS5.UsingPeerID)

	availableProxies, err = peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 1)

	// allow from peer1, check that peer2 got our config
	err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer2.PeerID(),
		Alias:                peer2Config.Alias,
		DomainName:           peer2Config.DomainName,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		peer1Config, err := peer2.api.KnownPeerConfig(peer1.PeerID())
		ts.NoError(err)

		return peer1Config.AllowedUsingAsExitNode && peer1Config.WeAllowUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	ts.Equal(peer1.PeerID(), peer2.app.Conf.SOCKS5.UsingPeerID)

	// disallow from peer2, check that peer1 got our new config
	err = peer2.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer1.PeerID(),
		Alias:                peer1Config.Alias,
		DomainName:           peer1Config.DomainName,
		AllowUsingAsExitNode: false,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)

		return !peer2Config.AllowedUsingAsExitNode && peer2Config.WeAllowUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	ts.Equal("", peer1.app.Conf.SOCKS5.UsingPeerID)

	availableProxies, err = peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 0)

	testSOCKS5Proxy(ts, peer1.app.Conf.SOCKS5.ListenAddress, fmt.Sprintf("%s %s", "unknown error", "general SOCKS server failure"))

	testSOCKS5Proxy(ts, peer2.app.Conf.SOCKS5.ListenAddress, fmt.Sprintf("%s %s", "unknown error", "connection not allowed by ruleset"))

	peer1.app.SOCKS5.SetProxyingLocalhostEnabled(true)
	testSOCKS5Proxy(ts, peer2.app.Conf.SOCKS5.ListenAddress, "")
	peer1.app.SOCKS5.SetProxyingLocalhostEnabled(false)

	// Testing API
	err = peer1.api.UpdateProxySettings(peer2.PeerID())
	ts.ErrorContains(err, "peer doesn't allow using as exit node")

	err = peer2.api.UpdateProxySettings("asd")
	ts.ErrorContains(err, "peer not found")

	info, err = peer2.api.PeerInfo()
	ts.NoError(err)
	ts.Equal(peer1.PeerID(), info.SOCKS5.UsingPeerID)

	err = peer2.api.UpdateProxySettings("")
	ts.NoError(err)

	info, err = peer2.api.PeerInfo()
	ts.NoError(err)
	ts.Equal("", info.SOCKS5.UsingPeerID)
}

func testSOCKS5Proxy(ts *TestSuite, proxyAddr string, expectSocksErr string) {
	// setup mock server
	expectedBody := strings.Repeat("test text", 10_000)
	addr := pickFreeAddr(ts.t)
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, expectedBody)
	})
	//nolint
	httpServer := &http.Server{Addr: addr, Handler: mux}
	go func() {
		_ = httpServer.ListenAndServe()
	}()
	defer func() {
		httpServer.Shutdown(context.Background())
	}()

	// client
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, nil)
	ts.NoError(err)
	httpTransport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
	httpClient := http.Client{Transport: httpTransport}

	// test
	for range 20 {
		response, err := httpClient.Get(fmt.Sprintf("http://%s/test", addr))
		if expectSocksErr != "" {
			ts.Error(err)

			var urlErr *url.Error
			ts.ErrorAs(err, &urlErr)
			var netErr *net.OpError
			ts.ErrorAs(urlErr.Err, &netErr)

			ts.Equal("socks connect", netErr.Op)
			ts.EqualError(netErr.Err, expectSocksErr)

			continue
		}

		ts.NoError(err)
		body, err := io.ReadAll(response.Body)
		ts.NoError(err)
		err = response.Body.Close()
		ts.NoError(err)

		ts.Equal(expectedBody, string(body))
	}
}

func TestTunnelPackets(t *testing.T) {
	if israce.Enabled && runtime.GOOS == "windows" {
		t.Skip("race mode on windows is too slow for this test")
	}

	ts := NewTestSuite(t)

	peer1 := ts.newTestPeer(false)
	peer2 := ts.newTestPeer(false)

	ts.makeFriends(peer2, peer1)

	current := goleak.IgnoreCurrent()
	goleak.VerifyNone(t, current)

	const packetSize = 2500
	const packetsCount = 2600 // approx 1.1 p2p streams

	peer1.tun.ReferenceInboundPacketLen = packetSize
	peer2.tun.ReferenceInboundPacketLen = packetSize

	wg := &sync.WaitGroup{}

	sendPackets := func(peer, peerWithInbound testPeer) {
		defer wg.Done()
		packet := testPacket(packetSize)

		for i := 0; i < packetsCount; i++ {
			peer.tun.Outbound <- packet
			// to don't have packets loss
			inbound := peerWithInbound.tun.InboundCount()
			if (int64(i) - inbound) >= 50 {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	wg.Add(2)
	go sendPackets(peer1, peer2)
	go sendPackets(peer2, peer1)
	wg.Wait()

	time.Sleep(1 * time.Second)
	received1 := peer1.tun.InboundCount()
	received2 := peer2.tun.InboundCount()
	ts.EqualValues(packetsCount, received1)
	ts.EqualValues(packetsCount, received2)
}

func BenchmarkTunnelPackets(b *testing.B) {
	packetSizes := []int{40, 300, 800, 1300, 1800, 2300, 2800, 3500}
	for _, packetSize := range packetSizes {
		b.Run(fmt.Sprintf("%d bytes per package", packetSize), func(b *testing.B) {
			ts := NewTestSuite(b)

			peer1 := ts.newTestPeer(true)
			peer2 := ts.newTestPeer(true)

			ts.makeFriends(peer2, peer1)
			b.ResetTimer()

			b.SetBytes(int64(packetSize))
			var packetsSent int64
			packet := testPacket(packetSize)
			peer2.tun.ReferenceInboundPacketLen = len(packet)
			peer2.tun.ClearInboundCount()
			for i := 0; i < b.N; i++ {
				peer1.tun.Outbound <- packet
				atomic.AddInt64(&packetsSent, 1)
				// to have packet_loss at reasonable level (but more than 0)
				const sleepEvery = 100
				if i != 0 && i%sleepEvery == 0 {
					time.Sleep(1 * time.Millisecond)
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
	vpnPacket.RecalculateChecksum()

	return vpnPacket.Packet
}

// TODO: add support for goleak in TestSuite
type TestSuite struct {
	*require.Assertions

	t                 testing.TB
	bootstrapAddrs    []peer.AddrInfo
	bootstrapAddrsStr []string
}

func NewTestSuite(t testing.TB) *TestSuite {
	if os.Getenv("CI") != "" && runtime.GOOS == "linux" {
		t.Skip("doesn't work on linux in CI, flaky ensurePeersAvailableInDHT can't find peers")
	}

	ts := &TestSuite{t: t, Assertions: require.New(t)}
	ts.initBootstrapNode()
	ts.initBootstrapNode()

	return ts
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

func (ts *TestSuite) newTestPeer(disableLogging bool) testPeer {
	tempDir := ts.t.TempDir()
	ts.t.Setenv(config.AppDataDirEnvKey, tempDir)
	tempConf := config.NewConfig(eventbus.NewBus())
	if disableLogging {
		tempConf.LoggerLevel = "fatal"
		log.SetAllLoggers(log.LevelFatal)
	}
	tempConf.Save()

	app := New()
	app.SetupLoggerAndConfig()
	if disableLogging {
		log.SetupLogging(zapcore.NewNopCore(), func(string) zapcore.Level {
			return zapcore.FatalLevel
		})
	}
	app.Conf.HttpListenAddress = "127.0.0.1:0"
	app.Conf.HttpListenOnAdminHost = false
	app.Conf.SetListenAddresses([]multiaddr.Multiaddr{
		multiaddr.StringCast("/ip4/127.0.0.1/tcp/0"),
		multiaddr.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"),
	})
	app.Conf.P2pNode.BootstrapPeers = ts.bootstrapAddrsStr
	app.Conf.SOCKS5 = config.SOCKS5Config{
		ListenerEnabled: true,
		ProxyingEnabled: true,
		ListenAddress:   pickFreeAddr(ts.t),
		UsingPeerID:     "",
	}

	testTUN := NewTestTUN()
	err := app.Init(context.Background(), testTUN.TUN())
	ts.NoError(err)

	tp := testPeer{
		app: app,
		api: apiclient.New(app.Api.Address()),
		tun: testTUN,
	}

	ts.t.Cleanup(func() {
		tp.Close()
	})

	return tp
}

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
		UserAgent:      config.UserAgent,
		BootstrapPeers: ts.bootstrapAddrs,
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

func (ts *TestSuite) ensurePeersAvailableInDHT(peer1, peer2 testPeer) {
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

func (ts *TestSuite) makeFriends(peer1, peer2 testPeer) {
	ts.ensurePeersAvailableInDHT(peer1, peer2)
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
