package entity

import (
	"time"

	"github.com/anywherelan/awl/protocol"
	"github.com/libp2p/go-libp2p-core/metrics"
)

// Requests
type (
	FriendRequest struct {
		PeerID string `validate:"required"`
		Alias  string
	}
	PeerIDRequest struct {
		PeerID string `validate:"required"`
	}
	UpdatePeerSettingsRequest struct {
		PeerID string `validate:"required"`
		Alias  string
	}
	UpdateMySettingsRequest struct {
		Name string
	}
)

// Responses
type (
	KnownPeersResponse struct {
		PeerID       string
		Name         string
		Version      string
		IpAddr       string
		Connected    bool
		Confirmed    bool
		LastSeen     time.Time
		Addresses    []string
		NetworkStats metrics.Stats
	}
	PeerInfo struct {
		PeerID                  string
		Name                    string
		Uptime                  time.Duration `swaggertype:"primitive,integer"`
		ServerVersion           string
		NetworkStats            metrics.Stats
		TotalBootstrapPeers     int
		ConnectedBootstrapPeers int
	}

	AuthRequest struct {
		PeerID string
		protocol.AuthPeer
	}
)

type (
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
