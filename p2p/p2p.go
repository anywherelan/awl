package p2p

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/multierr"
)

const (
	DesiredRelays  = 2
	RelayBootDelay = 20 * time.Second

	DHTProtocolPrefix protocol.ID = "/awl"

	protectedBootstrapPeerTag = "bootstrap"
	protectedPeerTag          = "known"

	// Port is unassigned by IANA and seems quite unused.
	// https://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.txt
	defaultP2pPort = 4363
)

type HostConfig struct {
	PrivKeyBytes   []byte
	ListenAddrs    []multiaddr.Multiaddr
	UserAgent      string
	BootstrapPeers []peer.AddrInfo

	Libp2pOpts  []libp2p.Option
	ConnManager struct {
		LowWater    int
		HighWater   int
		GracePeriod time.Duration
	}
	Peerstore    peerstore.Peerstore
	DHTDatastore ds.Batching
	DHTOpts      []dht.Option
}

type IDService interface {
	Close() error
	OwnObservedAddrs() []multiaddr.Multiaddr
	ObservedAddrsFor(local multiaddr.Multiaddr) []multiaddr.Multiaddr
	IdentifyConn(c network.Conn)
	IdentifyWait(c network.Conn) <-chan struct{}
}

type P2p struct {
	logger    *log.ZapEventLogger
	ctx       context.Context
	ctxCancel func()

	host             host.Host
	basicHost        *basichost.BasicHost
	dht              *dht.IpfsDHT
	bandwidthCounter metrics.Reporter
	connManager      *connmgr.BasicConnMgr
	bootstrapPeers   []peer.AddrInfo
	startedAt        time.Time
	bootstrapsInfo   atomic.Pointer[map[string]BootstrapPeerDebugInfo]
}

func NewP2p(ctx context.Context) *P2p {
	newCtx, ctxCancel := context.WithCancel(ctx)
	return &P2p{
		ctx:       newCtx,
		ctxCancel: ctxCancel,
		logger:    log.Logger("awl/p2p"),
	}
}

func (p *P2p) InitHost(hostConfig HostConfig) (host.Host, error) {
	var privKey crypto.PrivKey
	var err error
	if hostConfig.PrivKeyBytes == nil {
		privKey, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, err
		}
	} else {
		privKey, err = crypto.UnmarshalEd25519PrivateKey(hostConfig.PrivKeyBytes)
		if err != nil {
			return nil, err
		}
	}

	p.bandwidthCounter = metrics.NewBandwidthCounter()
	p.bootstrapPeers = hostConfig.BootstrapPeers

	p.connManager, err = connmgr.NewConnManager(
		hostConfig.ConnManager.LowWater,
		hostConfig.ConnManager.HighWater,
		connmgr.WithGracePeriod(hostConfig.ConnManager.GracePeriod),
	)
	if err != nil {
		return nil, fmt.Errorf("new conn manager: %v", err)
	}

	listenAddrs := hostConfig.ListenAddrs
	if len(listenAddrs) == 0 {
		listenAddrs = findListenAddrs()
	}

	p2pHost, err := libp2p.New(
		libp2p.Peerstore(hostConfig.Peerstore),
		libp2p.Identity(privKey),
		libp2p.UserAgent(hostConfig.UserAgent),
		libp2p.BandwidthReporter(p.bandwidthCounter),
		libp2p.ConnectionManager(p.connManager),
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.ChainOptions(
			libp2p.Transport(libp2pquic.NewTransport),
			libp2p.Transport(tcp.NewTCPTransport),
		),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			opts := []dht.Option{
				dht.Datastore(hostConfig.DHTDatastore),
				dht.ProtocolPrefix(DHTProtocolPrefix),
				dht.BootstrapPeers(p.bootstrapPeers...),
			}
			opts = append(opts, hostConfig.DHTOpts...)
			kademliaDHT, err := dht.New(p.ctx, h, opts...)
			p.dht = kademliaDHT
			p.basicHost = h.(*basichost.BasicHost)
			return p.dht, err
		}),
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
		libp2p.ChainOptions(hostConfig.Libp2pOpts...),
	)
	if err != nil {
		return nil, err
	}
	p.host = p2pHost
	p.startedAt = time.Now()

	return p2pHost, nil
}

func (p *P2p) Close() error {
	p.ctxCancel()
	err := multierr.Append(
		p.dht.Close(),
		p.host.Close(),
	)
	return err
}

func (p *P2p) PeerID() peer.ID {
	return p.host.ID()
}

func (p *P2p) Host() host.Host {
	return p.host
}

func (p *P2p) IDService() IDService {
	return p.basicHost.IDService()
}

func (p *P2p) ClearBackoff(peerID peer.ID) {
	p.host.Network().(*swarm.Swarm).Backoff().Clear(peerID)
}

func (p *P2p) ConnectPeer(ctx context.Context, peerID peer.ID) error {
	if p.IsConnected(peerID) {
		return nil
	}
	peerInfo, err := p.FindPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("could not find peer %s: %v", peerID.String(), err)
	}
	err = p.host.Connect(ctx, peerInfo)

	return err
}

func (p *P2p) FindPeer(ctx context.Context, id peer.ID) (peer.AddrInfo, error) {
	return p.dht.FindPeer(ctx, id)
}

func (p *P2p) NewStream(ctx context.Context, id peer.ID, proto protocol.ID) (network.Stream, error) {
	ctx = network.WithUseTransient(ctx, "awl")
	return p.host.NewStream(ctx, id, proto)
}

func (p *P2p) IsConnected(peerID peer.ID) bool {
	return p.host.Network().Connectedness(peerID) == network.Connected
}

func (p *P2p) ProtectPeer(id peer.ID) {
	p.host.ConnManager().Protect(id, protectedPeerTag)
}

func (p *P2p) UnprotectPeer(id peer.ID) {
	p.host.ConnManager().Unprotect(id, protectedPeerTag)
}

func (p *P2p) SubscribeConnectionEvents(onConnected, onDisconnected func(network.Network, network.Conn)) {
	notifyBundle := &network.NotifyBundle{
		ConnectedF:    onConnected,
		DisconnectedF: onDisconnected,
	}
	p.host.Network().Notify(notifyBundle)
}

func (p *P2p) Bootstrap() error {
	p.logger.Debug("Bootstrapping the DHT")
	// connect to the bootstrap nodes first
	ctx, cancel := context.WithTimeout(p.ctx, 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup

	for _, peerAddr := range p.bootstrapPeers {
		wg.Add(1)
		peerAddr := peerAddr
		p.host.ConnManager().Protect(peerAddr.ID, protectedBootstrapPeerTag)

		go func() {
			defer wg.Done()
			if err := p.host.Connect(ctx, peerAddr); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.Warnf("Failed to connect to bootstrap node %s: %v", peerAddr.ID, err)
			} else if err == nil {
				p.logger.Infof("Connection established with bootstrap node: %s", peerAddr.ID)
			}
		}()
	}
	wg.Wait()
	p.logger.Info("Connection established with all bootstrap nodes")

	if err := p.dht.Bootstrap(p.ctx); err != nil {
		return fmt.Errorf("bootstrap dht: %v", err)
	}

	return nil
}

func (p *P2p) MaintainBackgroundConnections(ctx context.Context, interval time.Duration, knownPeersIdsFunc func() []peer.ID) {
	const firstTryInterval = 5 * time.Second
	p.connectToKnownPeers(ctx, firstTryInterval, knownPeersIdsFunc())
	select {
	case <-ctx.Done():
		return
	case <-time.After(firstTryInterval):
	}
	p.connectToKnownPeers(ctx, interval, knownPeersIdsFunc())

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		p.connectToKnownPeers(ctx, interval, knownPeersIdsFunc())
	}
}

func (p *P2p) connectToKnownPeers(ctx context.Context, timeout time.Duration, peerIds []peer.ID) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, peerID := range peerIds {
		wg.Add(1)
		p.ProtectPeer(peerID)
		go func(peerID peer.ID) {
			defer wg.Done()
			_ = p.ConnectPeer(ctx, peerID)
		}(peerID)
	}

	bootstrapsInfo := make(map[string]BootstrapPeerDebugInfo)
	var mu sync.Mutex

	for _, peerAddr := range p.bootstrapPeers {
		wg.Add(1)
		peerAddr := peerAddr
		go func() {
			defer wg.Done()
			err := p.host.Connect(ctx, peerAddr)
			var info BootstrapPeerDebugInfo
			if err != nil {
				info.Error = err.Error()
			}
			info.Connections = p.peerAddressesString(peerAddr.ID)
			mu.Lock()
			bootstrapsInfo[peerAddr.ID.String()] = info
			mu.Unlock()
		}()
	}

	wg.Wait()

	p.bootstrapsInfo.Store(&bootstrapsInfo)
}

func (p *P2p) connsToPeer(peerID peer.ID) []network.Conn {
	return p.host.Network().ConnsToPeer(peerID)
}

func (p *P2p) peerAddressesString(peerID peer.ID) []string {
	conns := p.connsToPeer(peerID)
	addrs := make([]string, 0, len(conns))
	for _, conn := range conns {
		addrs = append(addrs, conn.RemoteMultiaddr().String())
	}
	return addrs
}

func findListenAddrs() []multiaddr.Multiaddr {
	// check if default port is open on tcp and udp
	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: defaultP2pPort})
	if err != nil {
		return UnicastListenAddrs()
	}
	_ = tcpListener.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: defaultP2pPort})
	if err != nil {
		return UnicastListenAddrs()
	}
	_ = udpConn.Close()

	return DefaultListenAddrs()
}

func UnicastListenAddrs() []multiaddr.Multiaddr {
	return []multiaddr.Multiaddr{
		multiaddr.StringCast("/ip4/0.0.0.0/tcp/0"),
		multiaddr.StringCast("/ip6/::/tcp/0"),
		multiaddr.StringCast("/ip4/0.0.0.0/udp/0/quic-v1"),
		multiaddr.StringCast("/ip6/::/udp/0/quic-v1"),
	}
}

func DefaultListenAddrs() []multiaddr.Multiaddr {
	return []multiaddr.Multiaddr{
		multiaddr.StringCast(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", defaultP2pPort)),
		multiaddr.StringCast(fmt.Sprintf("/ip6/::/tcp/%d", defaultP2pPort)),
		multiaddr.StringCast(fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", defaultP2pPort)),
		multiaddr.StringCast(fmt.Sprintf("/ip6/::/udp/%d/quic-v1", defaultP2pPort)),
	}
}
