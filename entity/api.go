package entity

import (
	"time"

	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/protocol"
	"github.com/libp2p/go-libp2p-core/metrics"
)

// Requests
type (
	FriendRequest struct {
		PeerID string `validate:"required"`
		Alias  string
	}
	FriendRequestReply struct {
		PeerID  string `validate:"required"`
		Alias   string
		Decline bool
	}
	PeerIDRequest struct {
		PeerID string `validate:"required"`
	}
	UpdatePeerSettingsRequest struct {
		PeerID     string `validate:"required"`
		Alias      string
		DomainName string
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
		DomainName   string
		Connected    bool
		Confirmed    bool
		Declined     bool
		LastSeen     time.Time
		Connections  []p2p.ConnectionInfo
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
		Reachability            string `enums:"Unknown,Public,Private"`
		AwlDNSAddress           string
		IsAwlDNSSetAsSystem     bool
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
		Reachability        string `enums:"Unknown,Public,Private"`
		ListenAddress       []string
		PeersWithAddrsCount int
		ObservedAddrs       []string
		BootstrapPeers      map[string]p2p.BootstrapPeerDebugInfo
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
