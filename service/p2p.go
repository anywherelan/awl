package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/p2p"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/metrics"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	protectedPeerTag = "known"
)

type P2pService struct {
	p2pServer          *p2p.P2p
	conf               *config.Config
	logger             *log.ZapEventLogger
	startedAt          time.Time
	bootstrapsInfo     map[string]entity.BootstrapPeerDebugInfo
	onPeerConnected    []func(peer.ID, network.Conn)
	onPeerDisconnected []func(peer.ID, network.Conn)
}

func NewP2p(server *p2p.P2p, conf *config.Config) *P2pService {
	p := &P2pService{
		p2pServer:      server,
		conf:           conf,
		logger:         log.Logger("awl/service/p2p"),
		startedAt:      time.Now(),
		bootstrapsInfo: make(map[string]entity.BootstrapPeerDebugInfo),
	}
	p.RegisterOnPeerConnected(func(peerID peer.ID, _ network.Conn) {
		p.conf.UpdatePeerLastSeen(peerID.String())
	})
	server.SubscribeConnectionEvents(p.onConnected, p.onDisconnected)

	// Protect friendly peers from disconnecting
	conf.RLock()
	for _, knownPeer := range conf.KnownPeers {
		p.ProtectPeer(knownPeer.PeerId())
	}

	for _, peerAddr := range conf.GetBootstrapPeers() {
		p.ProtectPeer(peerAddr.ID)
	}
	conf.RUnlock()

	return p
}

func (s *P2pService) ConnectPeer(ctx context.Context, peerID peer.ID) error {
	if s.IsConnected(peerID) {
		return nil
	}
	peerInfo, err := s.p2pServer.FindPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("could not find peer %s: %v", peerID.String(), err)
	}
	err = s.p2pServer.ConnectPeerAddr(ctx, peerInfo)

	return err
}

func (s *P2pService) NewStream(ctx context.Context, id peer.ID, proto protocol.ID) (network.Stream, error) {
	return s.p2pServer.NewStream(ctx, id, proto)
}

func (s *P2pService) StreamsToPeer(peerID peer.ID) []network.Stream {
	conns := s.p2pServer.ConnsToPeer(peerID)
	if len(conns) == 1 {
		return conns[0].GetStreams()
	}
	streams := make([]network.Stream, 0)
	for i := range conns {
		streams = append(streams, conns[i].GetStreams()...)
	}
	return streams
}

func (s *P2pService) PeerVersion(peerID peer.ID) string {
	return s.p2pServer.PeerVersion(peerID)
}

func (s *P2pService) IsConnected(peerID peer.ID) bool {
	return s.p2pServer.IsConnected(peerID)
}

func (s *P2pService) ProtectPeer(id peer.ID) {
	s.p2pServer.ChangeProtectedStatus(id, protectedPeerTag, true)
}

func (s *P2pService) UnprotectPeer(id peer.ID) {
	s.p2pServer.ChangeProtectedStatus(id, protectedPeerTag, false)
}

func (s *P2pService) PeerAddresses(peerID peer.ID) []string {
	conns := s.p2pServer.ConnsToPeer(peerID)
	addrs := make([]string, 0, len(conns))
	for _, conn := range conns {
		addrs = append(addrs, conn.RemoteMultiaddr().String())
	}
	return addrs
}

func (s *P2pService) PeerConnectionsInfo(peerID peer.ID) []entity.ConnectionInfo {
	conns := s.p2pServer.ConnsToPeer(peerID)
	infos := make([]entity.ConnectionInfo, 0, len(conns))
	for _, conn := range conns {
		addr := conn.RemoteMultiaddr()
		info, parsed := parseMultiaddrToInfo(addr)
		if !parsed {
			s.logger.DPanicf("could not parse multiaddr %s", addr)
			// still add unparsed info with multiaddr
		}
		infos = append(infos, info)
	}
	return infos
}

// BootstrapPeersStats returns total peers count and connected count.
func (s *P2pService) BootstrapPeersStats() (int, int) {
	connected := 0
	bootstrapPeers := s.conf.GetBootstrapPeers()
	for _, peerAddr := range bootstrapPeers {
		if s.p2pServer.IsConnected(peerAddr.ID) {
			connected += 1
		}
	}

	return len(bootstrapPeers), connected
}

func (s *P2pService) BootstrapPeersStatsDetailed() map[string]entity.BootstrapPeerDebugInfo {
	return s.bootstrapsInfo
}

func (s *P2pService) ConnectedPeersCount() int {
	return s.p2pServer.ConnectedPeersCount()
}

func (s *P2pService) RoutingTableSize() int {
	return s.p2pServer.RoutingTableSize()
}

func (s *P2pService) PeersWithAddrsCount() int {
	return s.p2pServer.PeersWithAddrsCount()
}

func (s *P2pService) AnnouncedAs() []ma.Multiaddr {
	return s.p2pServer.AnnouncedAs()
}

func (s *P2pService) OpenConnectionsCount() int {
	return s.p2pServer.OpenConnectionsCount()
}

func (s *P2pService) OpenStreamsCount() int64 {
	return s.p2pServer.OpenStreamsCount()
}

func (s *P2pService) TotalStreamsInbound() int64 {
	return s.p2pServer.TotalStreamsInbound()
}

func (s *P2pService) TotalStreamsOutbound() int64 {
	return s.p2pServer.TotalStreamsOutbound()
}

func (s *P2pService) ConnectionsLastTrimAgo() time.Duration {
	lastTrim := s.p2pServer.ConnectionsLastTrim()
	if lastTrim.IsZero() {
		lastTrim = s.startedAt
	}
	return time.Since(lastTrim)
}

func (s *P2pService) Reachability() network.Reachability {
	return s.p2pServer.Reachability()
}

func (s *P2pService) ObservedAddrs() []ma.Multiaddr {
	addrs := s.p2pServer.OwnObservedAddrs()
	return addrs
}

func (s *P2pService) NetworkStats() metrics.Stats {
	return s.p2pServer.NetworkStats()
}

func (s *P2pService) NetworkStatsByProtocol() map[protocol.ID]metrics.Stats {
	return s.p2pServer.NetworkStatsByProtocol()
}

func (s *P2pService) NetworkStatsByPeer() map[peer.ID]metrics.Stats {
	return s.p2pServer.NetworkStatsByPeer()
}

func (s *P2pService) NetworkStatsForPeer(peerID peer.ID) metrics.Stats {
	return s.p2pServer.NetworkStatsForPeer(peerID)
}

func (s *P2pService) Uptime() time.Duration {
	return time.Since(s.startedAt)
}

func (s *P2pService) RegisterOnPeerConnected(f func(peer.ID, network.Conn)) {
	s.onPeerConnected = append(s.onPeerConnected, f)
}

func (s *P2pService) RegisterOnPeerDisconnected(f func(peer.ID, network.Conn)) {
	s.onPeerDisconnected = append(s.onPeerDisconnected, f)
}

func (s *P2pService) MaintainBackgroundConnections(ctx context.Context, interval time.Duration) {
	s.connectToKnownPeers(ctx, interval)
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	s.connectToKnownPeers(ctx, interval)

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		s.connectToKnownPeers(ctx, interval)
		s.p2pServer.TrimOpenConnections()
	}
}

func (s *P2pService) connectToKnownPeers(ctx context.Context, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, peerID := range s.conf.KnownPeersIds() {
		wg.Add(1)
		go func(peerID peer.ID) {
			wg.Done()
			_ = s.ConnectPeer(ctx, peerID)
		}(peerID)
	}

	bootstrapsInfo := make(map[string]entity.BootstrapPeerDebugInfo)
	var mu sync.Mutex

	for _, peerAddr := range s.conf.GetBootstrapPeers() {
		wg.Add(1)
		peerAddr := peerAddr
		go func() {
			defer wg.Done()
			err := s.p2pServer.ConnectPeerAddr(ctx, peerAddr)
			var info entity.BootstrapPeerDebugInfo
			if err != nil {
				info.Error = err.Error()
			}
			info.Connections = s.PeerAddresses(peerAddr.ID)
			mu.Lock()
			bootstrapsInfo[peerAddr.ID.String()] = info
			mu.Unlock()
		}()
	}

	wg.Wait()

	s.bootstrapsInfo = bootstrapsInfo
}

func (s *P2pService) onConnected(_ network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	for _, f := range s.onPeerConnected {
		f(peerID, conn)
	}
}

func (s *P2pService) onDisconnected(_ network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	for _, f := range s.onPeerDisconnected {
		f(peerID, conn)
	}
}

func parseMultiaddrToInfo(addr ma.Multiaddr) (entity.ConnectionInfo, bool) {
	info := entity.ConnectionInfo{Multiaddr: addr.String()}
	protocols := addr.Protocols()
	if len(protocols) == 2 && protocols[1].Code == ma.P_TCP {
		info.Protocol = protocols[1].Name
		ip, _ := addr.ValueForProtocol(protocols[0].Code)
		port, _ := addr.ValueForProtocol(protocols[1].Code)
		info.Address = net.JoinHostPort(ip, port)
	} else if len(protocols) == 3 && protocols[2].Code == ma.P_QUIC {
		info.Protocol = protocols[2].Name
		ip, _ := addr.ValueForProtocol(protocols[0].Code)
		port, _ := addr.ValueForProtocol(protocols[1].Code)
		info.Address = net.JoinHostPort(ip, port)
	} else if _, err := addr.ValueForProtocol(ma.P_CIRCUIT); err == nil {
		info.ThroughRelay = true
		info.RelayPeerID, _ = addr.ValueForProtocol(ma.P_P2P)
	} else {
		return info, false
	}
	return info, true
}
