package entity

import (
	"time"

	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/protocol"
	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/metrics"
)

// Requests.
type (
	LogRequest struct {
		StartFromHead bool `url:"from_head" query:"from_head"`
		LogsRows      int  `url:"logs" query:"logs" validate:"numeric,gte=0"`
	}
	FriendRequest struct {
		PeerID string `validate:"required"`
		Alias  string `validate:"required,trimmed_str_not_empty"`
	}
	FriendRequestReply struct {
		PeerID  string `validate:"required"`
		Alias   string `validate:"required,trimmed_str_not_empty"`
		Decline bool
	}
	PeerIDRequest struct {
		PeerID string `validate:"required"`
	}
	UpdatePeerSettingsRequest struct {
		PeerID               string `validate:"required"`
		Alias                string `validate:"required,trimmed_str_not_empty"`
		DomainName           string
		AllowUsingAsExitNode bool
	}
	UpdateMySettingsRequest struct {
		Name string
	}
)

// Responses.
type (
	KnownPeersResponse struct {
		PeerID                 string
		Name                   string // Deprecated: use DisplayName instead
		DisplayName            string // Deprecated: useless, equal to Alias all the time
		Alias                  string
		Version                string
		IpAddr                 string
		DomainName             string
		Connected              bool
		Confirmed              bool
		Declined               bool
		WeAllowUsingAsExitNode bool
		AllowedUsingAsExitNode bool
		LastSeen               time.Time
		Connections            []p2p.ConnectionInfo
		NetworkStats           metrics.Stats
		NetworkStatsInIECUnits StatsInUnits
	}

	PeerInfo struct {
		PeerID                  string
		Name                    string
		Uptime                  time.Duration `swaggertype:"primitive,integer"`
		ServerVersion           string
		NetworkStats            metrics.Stats
		NetworkStatsInIECUnits  StatsInUnits
		TotalBootstrapPeers     int
		ConnectedBootstrapPeers int
		Reachability            string `enums:"Unknown,Public,Private"`
		AwlDNSAddress           string
		IsAwlDNSSetAsSystem     bool
	}

	StatsInUnits struct {
		TotalIn  string
		TotalOut string
		RateIn   string
		RateOut  string
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
		Version string
		Uptime  string
	}
	DhtDebugInfo struct {
		RoutingTableSize    int
		RoutingTable        []kbucket.PeerInfo
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
