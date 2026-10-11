package p2p

import (
	"encoding/json"
	"net"
	"slices"
	"strings"
	"time"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	duration, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(duration)
	return nil
}

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
	Ping        Duration `json:",omitempty" swaggertype:"string"`
	// PeerstoreAddrs are the addresses a dial to the peer goes through
	PeerstoreAddrs []string `json:",omitempty"`
	// Reconnect is set while the peer cannot be reached
	Reconnect *ReconnectDebugInfo `json:",omitempty"`
}

// ReconnectDebugInfo is what MaintainBackgroundConnections remembers about a
// peer it could not reach.
type ReconnectDebugInfo struct {
	// Failures in a row
	Failures       int
	LastAttemptAgo Duration `swaggertype:"string"`
	// NextAttemptIn is zero when the peer is to be tried on the next round
	NextAttemptIn Duration `swaggertype:"string"`
	LastError     string
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

// HasDirectConnection reports whether at least one current connection to the
// given peer is direct (i.e. not via a libp2p circuit relay). Returns false if
// there are no connections at all.
func (p *P2p) HasDirectConnection(peerID peer.ID) bool {
	for _, info := range p.PeerConnectionsInfo(peerID) {
		if !info.ThroughRelay {
			return true
		}
	}
	return false
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

// PeerPublicIP returns the verified public IP of the directly-connected peer.
// It inspects active connections (skipping /p2p-circuit relay connections) and
// returns the IP from the first non-relay Conn's RemoteMultiaddr — that address
// is the one libp2p actually completed a handshake against, so it's reliable
// (unlike Peerstore entries, which include unverified self-reported addrs from
// identify). Returns "" if the peer is reachable only via a relay or is not
// connected; in that case the real public IP is not observable to us.
func (p *P2p) PeerPublicIP(peerID peer.ID) string {
	for _, conn := range p.connsToPeer(peerID) {
		addr := conn.RemoteMultiaddr()
		if _, err := addr.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
			continue
		}
		if v, err := addr.ValueForProtocol(multiaddr.P_IP4); err == nil {
			return v
		}
		if v, err := addr.ValueForProtocol(multiaddr.P_IP6); err == nil {
			return v
		}
	}
	return ""
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

// TCPDialStats returns the dial counters of the main host. The AutoNAT
// dial-back hosts are left out: they never reuse a port by design.
func (p *P2p) TCPDialStats() TCPDialStats {
	p.tcpDialersMu.Lock()
	defer p.tcpDialersMu.Unlock()
	for _, dialer := range p.tcpDialers {
		if network.Network(dialer.sw) == p.host.Network() {
			return dialer.stats()
		}
	}
	return TCPDialStats{}
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

func (p *P2p) ConfirmedAddrs() (reachable []multiaddr.Multiaddr, unreachable []multiaddr.Multiaddr, unknown []multiaddr.Multiaddr) {
	return p.basicHost.ConfirmedAddrs()
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

	infos := make(map[string]BootstrapPeerDebugInfo, len(*m))
	for _, peerAddr := range p.bootstrapPeers {
		info, ok := (*m)[peerAddr.ID.String()]
		if !ok {
			continue
		}
		info.PeerstoreAddrs = p.PeerstoreAddrs(peerAddr.ID)
		info.Reconnect = p.ReconnectInfo(peerAddr.ID)
		infos[peerAddr.ID.String()] = info
	}

	return infos
}

// PeerstoreAddrs returns the addresses we have for the peer, sorted: the ones a
// dial to it goes through, a /dnsaddr among them is yet to be resolved. They
// are kept in memory and expire, so there are none for a peer that has been
// away for a while.
func (p *P2p) PeerstoreAddrs(peerID peer.ID) []string {
	peerAddrs := p.host.Peerstore().Addrs(peerID)
	addrs := make([]string, 0, len(peerAddrs))
	for _, addr := range peerAddrs {
		addrs = append(addrs, addr.String())
	}
	slices.Sort(addrs)

	return addrs
}

// ReconnectInfo reports what MaintainBackgroundConnections remembers about a
// known or a bootstrap peer it could not reach. It is nil for a peer with
// nothing against it: one that is connected, or was not tried yet.
func (p *P2p) ReconnectInfo(peerID peer.ID) *ReconnectDebugInfo {
	state, ok := p.retries.state(peerID)
	if !ok || p.IsConnected(peerID) {
		return nil
	}

	now := time.Now()
	return &ReconnectDebugInfo{
		Failures:       state.failures,
		LastAttemptAgo: Duration(now.Sub(state.lastAttempt).Round(time.Millisecond)),
		NextAttemptIn:  Duration(max(0, state.next.Sub(now)).Round(time.Millisecond)),
		LastError:      state.lastErr,
	}
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
