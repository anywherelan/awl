package entity

import (
	"time"

	"github.com/libp2p/go-libp2p-core/metrics"
	"github.com/peerlan/peerlan/config"
	"github.com/peerlan/peerlan/protocol"
)

// Requests
type (
	ConnectPeerRequest struct {
		PeerID     string `validate:"required"`
		LocalPort  int    `validate:"required"`
		RemotePort int    `validate:"required"`
		Protocol   string `validate:"required"`
	}
	FriendRequest struct {
		PeerID string `validate:"required"`
		Alias  string
	}
	PeerIDRequest struct {
		PeerID string `validate:"required"`
	}
	UpdatePeerSettingsRequest struct {
		PeerID      string `validate:"required"`
		Alias       string
		LocalConns  map[int]config.LocalConnConfig
		RemoteConns map[int]config.RemoteConnConfig
	}
	UpdateMySettingsRequest struct {
		Name string
	}
)

// Responses
type (
	KnownPeersResponse struct {
		PeerID             string
		Name               string
		Version            string
		IpAddr             string
		Connected          bool
		Confirmed          bool
		LastSeen           time.Time
		Addresses          []string
		NetworkStats       metrics.Stats
		AllowedLocalPorts  []int
		AllowedRemotePorts []int
	}
	PeerInfo struct {
		PeerID                  string
		Name                    string
		Uptime                  time.Duration
		ServerVersion           string
		NetworkStats            metrics.Stats
		TotalBootstrapPeers     int
		ConnectedBootstrapPeers int
	}

	ForwardedPort struct {
		RemotePort    int
		ListenAddress string
		PeerID        string
	}
	InboundStream struct {
		LocalPort int
		PeerID    string
		Protocol  string
	}

	AuthRequest struct {
		PeerID string
		protocol.AuthPeer
	}

	GetInboundConnectionsResponse map[int][]InboundStream

	P2pDebugInfo struct {
		General     GeneralDebugInfo
		DHT         DhtDebugInfo
		Connections ConnectionsDebugInfo
		Bandwidth   BandwidthDebugInfo
	}

	GeneralDebugInfo struct {
		Uptime string
	}
	DhtDebugInfo struct {
		RoutingTableSize    int
		Reachability        string
		ListenAddress       []string
		PeersWithAddrsCount int
		ObservedAddrs       []string
		BootstrapPeers      map[string]BootstrapPeerDebugInfo
	}
	BootstrapPeerDebugInfo struct {
		Error       string   `json:",omitempty"`
		Connections []string `json:",omitempty"`
	}
	ConnectionsDebugInfo struct {
		ConnectedPeersCount  int
		OpenConnectionsCount int
		OpenStreamsCount     int64
		TotalStreamsInbound  int64
		TotalStreamsOutbound int64
		LastTrimAgo          string
	}
	BandwidthDebugInfo struct {
		Total      BandwidthInfo
		ByProtocol map[string]BandwidthInfo
	}
	BandwidthInfo struct {
		TotalIn  string
		TotalOut string
		RateIn   string
		RateOut  string
	}
)
