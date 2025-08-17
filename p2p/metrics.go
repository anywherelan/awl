package p2p

import (
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

type ConnectionInfo struct {
	Multiaddr    string
	ThroughRelay bool
	RelayPeerID  string
	Address      string
	Protocol     string
	Direction    string
	Opened       time.Time
	Transient    bool
}

type BootstrapPeerDebugInfo struct {
	Error       string   `json:",omitempty"`
	Connections []string `json:",omitempty"`
}

func (p *P2p) Uptime() time.Duration {
	return time.Since(p.startedAt)
}

func (p *P2p) PeerUserAgent(peerID peer.ID) string {
	version, _ := p.host.Peerstore().Get(peerID, "AgentVersion")

	if version != nil {
		return version.(string)
	}

	return ""
}

func (p *P2p) PeerConnectionsInfo(peerID peer.ID) []ConnectionInfo {
	conns := p.connsToPeer(peerID)
	infos := make([]ConnectionInfo, 0, len(conns))
	for _, conn := range conns {
		addr := conn.RemoteMultiaddr()
		info, parsed := parseMultiaddrToInfo(addr)
		if !parsed {
			p.logger.DPanicf("could not parse multiaddr %s", addr)
			// still add unparsed info with multiaddr
		}
		stat := conn.Stat()
		info.Direction = strings.ToLower(stat.Direction.String())
		info.Opened = stat.Opened
		info.Transient = stat.Limited
		infos = append(infos, info)
	}
	return infos
}

func (p *P2p) ConnectedPeersCount() int {
	return len(p.host.Network().Peers())
}

func (p *P2p) RoutingTableSize() int {
	return p.dht.RoutingTable().Size()
}

func (p *P2p) RoutingTablePeers() []kbucket.PeerInfo {
	return p.dht.RoutingTable().GetPeerInfos()
}

func (p *P2p) NetworkSize() (int32, error) {
	return p.dht.NetworkSize()
}

func (p *P2p) PeersWithAddrsCount() int {
	return len(p.host.Peerstore().PeersWithAddrs())
}

func (p *P2p) AnnouncedAs() []multiaddr.Multiaddr {
	return p.host.Addrs()
}

func (p *P2p) Reachability() network.Reachability {
	return p.basicHost.Reachability()
}

func (p *P2p) OpenConnectionsCount() int {
	return p.connManager.GetInfo().ConnCount
}

func (p *P2p) OpenStreamsCount() int64 {
	count := int64(0)
	conns := p.host.Network().Conns()
	for _, conn := range conns {
		stats := conn.Stat()
		count += int64(stats.NumStreams)
	}

	return count
}

func (p *P2p) OpenStreamStats() map[protocol.ID]map[string]int {
	stats := make(map[protocol.ID]map[string]int)

	for _, conn := range p.host.Network().Conns() {
		for _, stream := range conn.GetStreams() {
			direction := strings.ToLower(stream.Stat().Direction.String())
			protocolStats, ok := stats[stream.Protocol()]
			if !ok {
				protocolStats = make(map[string]int)
				stats[stream.Protocol()] = protocolStats
			}
			protocolStats[direction]++
		}
	}

	return stats
}

func (p *P2p) ConnectionsLastTrimAgo() time.Duration {
	lastTrim := p.connManager.GetInfo().LastTrim
	if lastTrim.IsZero() {
		lastTrim = p.startedAt
	}
	return time.Since(lastTrim)
}

func (p *P2p) OwnObservedAddrs() []multiaddr.Multiaddr {
	return p.basicHost.IDService().OwnObservedAddrs()
}

func (p *P2p) NetworkStats() metrics.Stats {
	return p.bandwidthCounter.GetBandwidthTotals()
}

func (p *P2p) NetworkStatsByProtocol() map[protocol.ID]metrics.Stats {
	return p.bandwidthCounter.GetBandwidthByProtocol()
}

func (p *P2p) NetworkStatsByPeer() map[peer.ID]metrics.Stats {
	return p.bandwidthCounter.GetBandwidthByPeer()
}

func (p *P2p) NetworkStatsForPeer(peerID peer.ID) metrics.Stats {
	return p.bandwidthCounter.GetBandwidthForPeer(peerID)
}

// BootstrapPeersStats returns total peers count and connected count.
func (p *P2p) BootstrapPeersStats() (int, int) {
	connected := 0
	for _, peerAddr := range p.bootstrapPeers {
		if p.IsConnected(peerAddr.ID) {
			connected += 1
		}
	}

	return len(p.bootstrapPeers), connected
}

func (p *P2p) BootstrapPeersStatsDetailed() map[string]BootstrapPeerDebugInfo {
	m := p.bootstrapsInfo.Load()
	if m == nil {
		return nil
	}
	return *m
}

func parseMultiaddrToInfo(addr multiaddr.Multiaddr) (ConnectionInfo, bool) {
	info := ConnectionInfo{Multiaddr: addr.String()}
	protocols := addr.Protocols()
	if len(protocols) == 2 && protocols[1].Code == multiaddr.P_TCP {
		info.Protocol = protocols[1].Name
		ip, _ := addr.ValueForProtocol(protocols[0].Code)
		port, _ := addr.ValueForProtocol(protocols[1].Code)
		info.Address = net.JoinHostPort(ip, port)
	} else if len(protocols) == 3 &&
		(protocols[2].Code == multiaddr.P_QUIC || protocols[2].Code == multiaddr.P_QUIC_V1) {
		info.Protocol = "quic"
		ip, _ := addr.ValueForProtocol(protocols[0].Code)
		port, _ := addr.ValueForProtocol(protocols[1].Code)
		info.Address = net.JoinHostPort(ip, port)
	} else if _, err := addr.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
		info.ThroughRelay = true
		info.RelayPeerID, _ = addr.ValueForProtocol(multiaddr.P_P2P)
	} else {
		return info, false
	}
	return info, true
}
