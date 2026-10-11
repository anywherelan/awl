package p2p

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
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
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	"github.com/multiformats/go-multiaddr"
	msmux "github.com/multiformats/go-multistream"

	awlmetrics "github.com/anywherelan/awl/metrics"
)

const (
	DesiredRelays  = 2
	RelayBootDelay = 20 * time.Second

	DHTProtocolPrefix protocol.ID = "/awl"

	protectedBootstrapPeerTag = "bootstrap"
	protectedPeerTag          = "known"

	// why the reconnection schedule is reset, a metric label
	networkBootstrapReturned = "bootstrap_returned"
	networkNotified          = "notified"

	// enoughBootstrapPeers is how many connected bootstrap peers make the
	// unreachable rest not worth trying every round: both the DHT and autorelay
	// want two.
	enoughBootstrapPeers = 2

	// Port is unassigned by IANA and seems quite unused.
	// https://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.txt
	defaultP2pPort = 4363
)

type HostConfig struct {
	PrivKeyBytes             []byte
	ListenAddrs              []multiaddr.Multiaddr
	UserAgent                string
	BootstrapPeers           []peer.AddrInfo
	AllowEmptyBootstrapPeers bool
	EnableAutoRelay          bool

	// SocketControlFunc is set when gateway mode is enabled to mark sockets
	// (e.g., SO_MARK on Linux) so they bypass the VPN TUN interface.
	SocketControlFunc func(network, address string, c syscall.RawConn) error

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
	// IdentifyConn synchronously triggers an identify request on the connection and
	// waits for it to complete. If the connection is being identified by another
	// caller, this call will wait. If the connection has already been identified,
	// it will return immediately.
	IdentifyConn(network.Conn)
	// IdentifyWait triggers an identify (if the connection has not already been
	// identified) and returns a channel that is closed when the identify protocol
	// completes.
	IdentifyWait(network.Conn) <-chan struct{}
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
	config           HostConfig
	bootstrapPeers   []peer.AddrInfo
	startedAt        time.Time
	bootstrapsInfo   atomic.Pointer[map[string]BootstrapPeerDebugInfo]

	// one per TCP transport instance: the main host plus the AutoNAT
	// dial-back hosts go-libp2p builds from the same transport options
	tcpDialersMu sync.Mutex
	tcpDialers   []*reuseportDialer

	// when to try the known and the bootstrap peers that could not be reached
	retries *retrySchedule
	// carries the reason, see NotifyNetworkChanged
	networkChanged chan string
	// known peers with a redial pending, see redialOnDisconnect
	redials sync.Map
	// not constants for the tests to shorten them
	maxRedialDelay  time.Duration
	firstRetryDelay time.Duration

	dhtBootstrapFinishedChan chan struct{}
}

func NewP2p(ctx context.Context) *P2p {
	newCtx, ctxCancel := context.WithCancel(ctx)
	return &P2p{
		ctx:       newCtx,
		ctxCancel: ctxCancel,
		logger:    log.Logger("awl/p2p"),

		retries:         newRetrySchedule(),
		networkChanged:  make(chan string, 1),
		maxRedialDelay:  defaultMaxRedialDelay,
		firstRetryDelay: defaultFirstRetryDelay,

		dhtBootstrapFinishedChan: make(chan struct{}),
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

	if !hostConfig.AllowEmptyBootstrapPeers && len(hostConfig.BootstrapPeers) == 0 {
		return nil, fmt.Errorf("zero bootstrap peers provided")
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

	if hostConfig.EnableAutoRelay {
		hostConfig.Libp2pOpts = append(hostConfig.Libp2pOpts,
			libp2p.EnableAutoRelayWithPeerSource(
				p.getAutoRelayPeerSource(),
				autorelay.WithNumRelays(DesiredRelays),
				autorelay.WithMinCandidates(DesiredRelays),
				autorelay.WithMaxCandidates(DesiredRelays),
				autorelay.WithBootDelay(RelayBootDelay),
			))
	}

	p.config = hostConfig

	transportOpts := p.buildTransportOpts(hostConfig.SocketControlFunc)

	p2pHost, err := libp2p.New(
		libp2p.Peerstore(hostConfig.Peerstore),
		libp2p.Identity(privKey),
		libp2p.UserAgent(hostConfig.UserAgent),
		libp2p.BandwidthReporter(p.bandwidthCounter),
		libp2p.ConnectionManager(p.connManager),
		libp2p.SwarmOpts(swarm.WithDialRanker(p.getDialRanker())),
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.ChainOptions(transportOpts...),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			opts := []dht.Option{
				dht.Datastore(hostConfig.DHTDatastore),
				dht.ProtocolPrefix(DHTProtocolPrefix),
				dht.BootstrapPeers(p.bootstrapPeers...),
			}
			opts = append(opts, hostConfig.DHTOpts...)
			kademliaDHT, err := dht.New(h, opts...)
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

// buildTransportOpts wires the socket-marking controlFunc into every place
// libp2p lets us: TCP dials (WithDialerForAddr) and UDP listen sockets
// (quicreuse.OverrideListenUDP — QUIC dials share the listen socket).
//
// WithDialerForAddr disables the stock reuseport dialing, so TCP dials go
// through reuseportDialer, which restores it. The TCP transport is built by
// our own constructor rather than by passing the option to libp2p.Transport:
// the dialer needs the swarm of the very transport it serves, and libp2p
// creates several from these options (see reuseportDialer).
//
// TODO(gateway): the TCP *listener* is NOT covered — go-libp2p's TCP
// transport has no listen hook (Listen goes straight to manet.Listen), so
// sockets accepted from it stay unmarked on every platform. With gateway
// client mode on, their replies are routed into the TUN and inbound direct
// TCP degrades (no leak; QUIC/dials/hole-punch/relay still work). Fixing it
// needs an upstream listen hook + marking the listener (accepted sockets
// inherit SO_MARK on Linux / listener options on Windows) — separate task
func (p *P2p) buildTransportOpts(controlFunc func(network, address string, c syscall.RawConn) error) []libp2p.Option {
	if controlFunc == nil {
		return []libp2p.Option{
			libp2p.Transport(libp2pquic.NewTransport),
			libp2p.Transport(tcp.NewTCPTransport),
		}
	}

	newTCPTransport := func(
		upgrader transport.Upgrader, rcmgr network.ResourceManager, sharedTCP *tcpreuse.ConnMgr, sw *swarm.Swarm,
	) (*tcp.TcpTransport, error) {
		dialer := &reuseportDialer{control: controlFunc, listenAddrs: sw.ListenAddresses, sw: sw}
		p.tcpDialersMu.Lock()
		p.tcpDialers = append(p.tcpDialers, dialer)
		p.tcpDialersMu.Unlock()

		tcpDialer := func(multiaddr.Multiaddr) (tcp.ContextDialer, error) {
			return dialer, nil
		}
		return tcp.NewTCPTransport(upgrader, rcmgr, sharedTCP, tcp.WithDialerForAddr(tcpDialer))
	}

	listenUDP := func(network string, laddr *net.UDPAddr) (net.PacketConn, error) {
		conn, err := net.ListenUDP(network, laddr)
		if err != nil {
			return nil, err
		}
		rawConn, err := conn.SyscallConn()
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if err := controlFunc(network, laddr.String(), rawConn); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}

	return []libp2p.Option{
		libp2p.QUICReuse(quicreuse.NewConnManager, quicreuse.OverrideListenUDP(listenUDP)),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(newTCPTransport),
	}
}

func (p *P2p) Close() error {
	p.ctxCancel()
	err := errors.Join(
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

	// FindPeer runs until peer is found in DHT or context is cancelled, so a timeout is mandatory
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

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
	ctx = network.WithAllowLimitedConn(ctx, "awl")
	return p.host.NewStream(ctx, id, proto)
}

func (p *P2p) NewStreamMulti(ctx context.Context, id peer.ID, protos ...protocol.ID) (network.Stream, error) {
	ctx = network.WithAllowLimitedConn(ctx, "awl")
	return p.host.NewStream(ctx, id, protos...)
}

func (p *P2p) NewStreamWithDedicatedConn(ctx context.Context, id peer.ID, proto protocol.ID) (network.Stream, error) {
	ctx = network.WithAllowLimitedConn(ctx, "awl")

	// mostly copied from NewStream()
	// github.com/libp2p/go-libp2p@v0.32.2/p2p/host/basic/basic_host.go:634
	conn, err := p.host.Network().DialPeer(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %v", err)
	}

	stream, err := conn.NewStream(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to create new stream: %v", err)
	}

	err = stream.SetProtocol(proto)
	if err != nil {
		return nil, fmt.Errorf("failed to set protocol to stream: %v", err)
	}
	lzcon := msmux.NewMSSelect(stream, proto)

	return &streamWrapper{
		Stream: stream,
		rw:     lzcon,
	}, nil
}

func (p *P2p) IsConnected(peerID peer.ID) bool {
	return p.host.Network().Connectedness(peerID) != network.NotConnected
}

func (p *P2p) ProtectPeer(id peer.ID) {
	p.host.ConnManager().Protect(id, protectedPeerTag)
}

func (p *P2p) UnprotectPeer(id peer.ID) {
	p.host.ConnManager().Unprotect(id, protectedPeerTag)
}

func (p *P2p) RecordPeerLatency(id peer.ID, rtt time.Duration) {
	p.host.Peerstore().RecordLatency(id, rtt)
}

func (p *P2p) GetPeerLatency(id peer.ID) time.Duration {
	return p.host.Peerstore().LatencyEWMA(id)
}

func (p *P2p) SubscribeConnectionEvents(onConnected, onDisconnected func(network.Network, network.Conn)) {
	notifyBundle := &network.NotifyBundle{
		ConnectedF:    onConnected,
		DisconnectedF: onDisconnected,
	}
	p.host.Network().Notify(notifyBundle)
}

func (p *P2p) Bootstrap() {
	ctx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	var wg sync.WaitGroup
	successfulConnectionsCh := make(chan struct{}, len(p.bootstrapPeers))

	p.logger.Debug("Start bootstrapping the DHT")
	for _, peerAddr := range p.bootstrapPeers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.basicHost.Connect(ctx, peerAddr); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.Warnf("Failed to connect to bootstrap node %s: %v", peerAddr.ID, err)
			} else if err == nil {
				p.logger.Infof("Connection established with bootstrap node: %s", peerAddr.ID)
				successfulConnectionsCh <- struct{}{}
			}
		}()
	}

	go func() {
		defer cancel()

		// wait for at least 2 bootstrap nodes
		for range min(2, len(p.bootstrapPeers)) {
			select {
			case <-ctx.Done():
			case <-successfulConnectionsCh:
			}
		}

		p.logger.Info("Bootstrapping the DHT")
		if err := p.dht.Bootstrap(p.ctx); err != nil {
			// from the code err is always nil for now
			p.logger.Warnf("Failed to bootstrap DHT: %v", err)
		}
		close(p.dhtBootstrapFinishedChan)

		wg.Wait()
		p.logger.Info("Finished connecting to all bootstrap nodes")
	}()
}

// KnownPeer is a peer MaintainBackgroundConnections keeps a connection to.
type KnownPeer struct {
	ID peer.ID
	// Eager peers are tried every round with no backoff, however long they have
	// been unreachable.
	Eager bool
}

// MaintainBackgroundConnections keeps the node connected to its known and
// bootstrap peers. A peer that cannot be reached is retried less and less
// often, see retrySchedule; interval is both the pace of the rounds and the
// first pause of that backoff.
func (p *P2p) MaintainBackgroundConnections(ctx context.Context, interval time.Duration, knownPeersFunc func() []KnownPeer) {
	const timeout = 5 * time.Second

	stopRedial := p.redialOnDisconnect(ctx, knownPeersFunc)
	defer stopRedial()

	// wait for bootstrapping
	select {
	case <-ctx.Done():
		return
	case <-p.dhtBootstrapFinishedChan:
	}

	round := func(force bool) (bootstrapBefore, bootstrapAfter int) {
		return p.connectToKnownPeers(ctx, timeout, interval, knownPeersFunc(), force)
	}
	// startOver is for the start and for every time the network comes back,
	// which is no different from a start: whatever the schedule remembers about
	// the peers is stale, so everyone is tried again.
	startOver := func() (bootstrapConnected int) {
		p.retries.reset()
		round(true)

		// retry once after a short delay in case of network instability
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(p.firstRetryDelay):
		}
		_, bootstrapConnected = round(true)

		return bootstrapConnected
	}

	bootstrapConnected := startOver()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-p.networkChanged:
			awlmetrics.P2PReconnectResetsTotal.WithLabelValues(reason).Inc()
			p.logger.Infof("reconnect schedule reset, retrying all known and bootstrap peers, reason: %s", reason)
			bootstrapConnected = startOver()
		case <-ticker.C:
			before, after := round(false)
			// Having no bootstrap peer at all and then getting one back is how
			// the return of the network looks from here
			if after > 0 && (before == 0 || bootstrapConnected == 0) {
				p.signalNetworkChanged(networkBootstrapReturned)
			}
			bootstrapConnected = after
		}
		ticker.Reset(interval)
	}
}

// NotifyNetworkChanged tells P2p that the network of the host has changed, for
// the case when the caller knows better than P2p can find out. Every known and
// bootstrap peer is then tried right away, with no regard to its backoff.
// It does not block, and calls that pile up are handled as one.
func (p *P2p) NotifyNetworkChanged() {
	p.signalNetworkChanged(networkNotified)
}

func (p *P2p) signalNetworkChanged(reason string) {
	select {
	case p.networkChanged <- reason:
	default:
	}
}

// connectToKnownPeers makes one round over the known and the bootstrap peers:
// tries the ones whose turn has come, or all of them with force, and pings the
// connected ones. It returns the number of connected bootstrap peers before and
// after the round.
func (p *P2p) connectToKnownPeers(
	ctx context.Context, timeout, retryBase time.Duration, knownPeers []KnownPeer, force bool,
) (bootstrapBefore, bootstrapAfter int) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	now := time.Now()
	var wg sync.WaitGroup
	for _, knownPeer := range knownPeers {
		wg.Add(1)
		p.ProtectPeer(knownPeer.ID)
		go func() {
			defer wg.Done()
			if force {
				p.ClearBackoff(knownPeer.ID)
			}
			attempted, err := p.tryConnect(knownPeer.ID, now, force || knownPeer.Eager, retryBase, func() error {
				return p.ConnectPeer(ctx, knownPeer.ID)
			})
			if !attempted || err != nil {
				return
			}

			p.pingPeer(ctx, knownPeer.ID)
		}()
	}

	_, bootstrapBefore = p.BootstrapPeersStats()
	lastBootstrapsInfo := p.bootstrapsInfo.Load()
	bootstrapsInfo := make(map[string]BootstrapPeerDebugInfo)
	var mu sync.Mutex

	for _, peerAddr := range p.bootstrapPeers {
		wg.Add(1)
		p.host.ConnManager().Protect(peerAddr.ID, protectedBootstrapPeerTag)

		go func() {
			defer wg.Done()

			// Short of bootstrap peers, each of them is tried every round: the
			// problem may well be our network. With enough of them connected the
			// network is evidently fine and it is the peer that is down, so it
			// waits for its turn like any other.
			needMore := bootstrapBefore < enoughBootstrapPeers
			if force || needMore {
				p.ClearBackoff(peerAddr.ID)
			}

			attempted, err := p.tryConnect(peerAddr.ID, now, force || needMore, retryBase, func() error {
				// Not p.host: the routed host answers a failed dial with a DHT
				// lookup of the peer, and the addresses of a bootstrap peer come
				// from the config, the DHT has nothing to add
				return p.basicHost.Connect(ctx, peerAddr)
			})

			var info BootstrapPeerDebugInfo
			if !attempted {
				// not its turn, keep what the last attempt reported
				if lastBootstrapsInfo != nil {
					info = (*lastBootstrapsInfo)[peerAddr.ID.String()]
				}
			} else {
				if err != nil {
					info.Error = err.Error()
				}
				info.Connections = p.peerAddressesString(peerAddr.ID)

				if err == nil {
					p.pingPeer(ctx, peerAddr.ID)
				}
				info.Ping = Duration(p.host.Peerstore().LatencyEWMA(peerAddr.ID))
			}

			mu.Lock()
			bootstrapsInfo[peerAddr.ID.String()] = info
			mu.Unlock()
		}()
	}

	wg.Wait()

	p.bootstrapsInfo.Store(&bootstrapsInfo)
	_, bootstrapAfter = p.BootstrapPeersStats()

	return bootstrapBefore, bootstrapAfter
}

// tryConnect runs connect for a peer that is not connected, provided its turn
// has come by the schedule or force is set. attempted is false when the
// schedule held the attempt back or a redial is on the peer; a peer that is
// connected already counts as attempted with no error.
func (p *P2p) tryConnect(
	peerID peer.ID, now time.Time, force bool, retryBase time.Duration, connect func() error,
) (attempted bool, err error) {
	if p.IsConnected(peerID) {
		p.retries.forget(peerID)
		return true, nil
	}
	if _, redialing := p.redials.Load(peerID); redialing {
		// leave it to the redial, see redialOnDisconnect
		return false, nil
	}
	if !force && !p.retries.due(peerID, now) {
		return false, nil
	}

	err = connect()
	if err != nil {
		p.retries.failed(peerID, time.Now(), retryBase, err)
	} else {
		p.retries.forget(peerID)
	}

	return true, err
}

func (p *P2p) pingPeer(ctx context.Context, peerID peer.ID) {
	const desiredPings = 5

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// TODO: also calc jitter
	ch := ping.Ping(ctx, p.host, peerID)
	pingCount := 0
	for range ch {
		pingCount++
		if pingCount >= desiredPings {
			return
		}
	}
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

func (p *P2p) getAutoRelayPeerSource() func(_ context.Context, numPeers int) <-chan peer.AddrInfo {
	// TODO: discover relays from non-default bootstrap peers

	getAllCandidates := func() []peer.AddrInfo {
		bootstrapPeers := slices.Clone(p.bootstrapPeers)
		if p.host == nil {
			return bootstrapPeers
		}
		ps := p.host.Peerstore()

		bootstrapPeers = slices.DeleteFunc(bootstrapPeers, func(info peer.AddrInfo) bool {
			return !p.IsConnected(info.ID) || ps.LatencyEWMA(info.ID) == 0
		})

		slices.SortFunc(bootstrapPeers, func(a, b peer.AddrInfo) int {
			return int(ps.LatencyEWMA(a.ID) - ps.LatencyEWMA(b.ID))
		})

		return bootstrapPeers
	}

	var lastAddrInfos []peer.AddrInfo
	return func(_ context.Context, numPeers int) <-chan peer.AddrInfo {
		if len(lastAddrInfos) == 0 {
			lastAddrInfos = getAllCandidates()
		}

		if len(lastAddrInfos) < numPeers {
			numPeers = len(lastAddrInfos)
		}
		ch := make(chan peer.AddrInfo, numPeers)
		defer close(ch)

		for i := 0; i < numPeers; i++ {
			ch <- lastAddrInfos[i]
		}
		lastAddrInfos = lastAddrInfos[numPeers:]

		return ch
	}
}

func (p *P2p) getDialRanker() network.DialRanker {
	return func(addrs []multiaddr.Multiaddr) []network.AddrDelay {
		defaultDelays := swarm.DefaultDialRanker(addrs)

		hasClosestRelay := false
		var closestRelayID peer.ID
		var closestLatency time.Duration

		// finds the closest relay and adds 500 ms delay to all other relay addresses
		for _, addrDelay := range defaultDelays {
			_, err := addrDelay.Addr.ValueForProtocol(multiaddr.P_CIRCUIT)
			if err != nil {
				continue
			}
			relayPIDString, err := addrDelay.Addr.ValueForProtocol(multiaddr.P_P2P)
			if err != nil {
				continue
			}
			relayPID, err := peer.Decode(relayPIDString)
			if err != nil {
				continue
			}

			latency := p.config.Peerstore.LatencyEWMA(relayPID)
			if latency != 0 && (!hasClosestRelay || latency < closestLatency) {
				closestRelayID = relayPID
				closestLatency = latency
				hasClosestRelay = true
			}
		}

		for i, addrDelay := range defaultDelays {
			_, err := addrDelay.Addr.ValueForProtocol(multiaddr.P_CIRCUIT)
			if err != nil {
				continue
			}

			relayPIDString, err := addrDelay.Addr.ValueForProtocol(multiaddr.P_P2P)
			if err != nil {
				continue
			}
			relayPID, err := peer.Decode(relayPIDString)
			if err != nil {
				continue
			}

			if hasClosestRelay && relayPID != closestRelayID {
				defaultDelays[i].Delay += 500 * time.Millisecond
			}
		}

		return defaultDelays
	}
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

// copied from
// github.com/libp2p/go-libp2p@v0.32.2/p2p/host/basic/basic_host.go:1050
type streamWrapper struct {
	network.Stream
	rw io.ReadWriteCloser
}

func (s *streamWrapper) Read(b []byte) (int, error) {
	return s.rw.Read(b)
}

func (s *streamWrapper) Write(b []byte) (int, error) {
	return s.rw.Write(b)
}

func (s *streamWrapper) Close() error {
	return s.rw.Close()
}

func (s *streamWrapper) CloseWrite() error {
	// Flush the handshake before closing, but ignore the error. The other
	// end may have closed their side for reading.
	//
	// If something is wrong with the stream, the user will get on error on
	// read instead.
	if flusher, ok := s.rw.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
	return s.Stream.CloseWrite()
}
