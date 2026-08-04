package entity

import (
	"time"

	kbucket "github.com/libp2p/go-libp2p-kbucket"
	"github.com/libp2p/go-libp2p/core/metrics"

	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/protocol"
)

// Requests
type (
	LogRequest struct {
		StartFromHead bool `url:"from_head" query:"from_head"`
		LogsRows      int  `url:"logs" query:"logs" validate:"numeric,gte=0"`
	}
	FriendRequest struct {
		PeerID string `validate:"required"`
		Alias  string `validate:"required,trimmed_str_not_empty,max=100,no_control_chars"`
		// optional: specific IP address for the peer
		IPAddr string `validate:"omitempty,ipv4"`
		// optional: allow the peer to use us as an exit node
		AllowUsingAsExitNode bool
		// optional: token from an invite link (see InviteLink.Token). It is
		// stored with the peer and presented in every auth request until the
		// peer confirms us, which it then does without a manual accept.
		Token string
	}
	FriendRequestReply struct {
		PeerID  string `validate:"required"`
		Alias   string `validate:"required,trimmed_str_not_empty,max=100,no_control_chars"`
		Decline bool
		// optional: specific IP address for the peer
		IPAddr string `validate:"omitempty,ipv4"`
		// optional: allow the peer to use us as an exit node
		AllowUsingAsExitNode bool
	}
	PeerIDRequest struct {
		PeerID string `validate:"required"`
	}
	UpdatePeerSettingsRequest struct {
		PeerID     string `validate:"required"`
		Alias      string `validate:"required,trimmed_str_not_empty,max=100,no_control_chars"`
		DomainName string `validate:"required,trimmed_str_not_empty"`
		// TODO: support ipv6
		IPAddr               string `validate:"required,ipv4"`
		AllowUsingAsExitNode bool
	}
	UpdateMySettingsRequest struct {
		Name string `validate:"max=100,no_control_chars"`
	}

	UpdateProxySettingsRequest struct {
		UsingPeerID string
	}

	CreateInviteRequest struct {
		// MaxUses: 0 means the default, 1 (single-use).
		MaxUses int `validate:"gte=0,lte=100"`
		// ExpiresInSeconds: 0 means the invite never expires. A number rather
		// than a duration string, as with ReconnectionIntervalSec; clients parse
		// "24h" on their side. Maximum is 365 days.
		ExpiresInSeconds int64 `validate:"gte=0,lte=31536000"`
		// Alias to give the peer that redeems the link. Single-use links only.
		Alias string `validate:"max=100,no_control_chars"`
		// AllowUsingAsExitNode lets the new peer use us as an exit node.
		AllowUsingAsExitNode bool
		Label                string `validate:"max=100,no_control_chars"`
	}
	RevokeInviteRequest struct {
		ID string `validate:"required"`
	}
)

// Responses
type (
	KnownPeersResponse struct {
		PeerID                        string
		Name                          string // Deprecated: use DisplayName instead
		DisplayName                   string // Deprecated: useless, equal to Alias all the time
		Alias                         string
		Version                       string
		IpAddr                        string
		IpAddrV6                      string
		DomainName                    string
		Connected                     bool
		Confirmed                     bool
		Declined                      bool
		WeAllowUsingAsExitNode        bool
		AllowedUsingAsExitNode        bool
		RemoteVPNGatewayServerEnabled bool
		// InviteID is set when we let this peer in through one of our invite
		// links; empty otherwise. Non-secret, see config.KnownPeer.InviteID.
		InviteID               string
		LastSeen               time.Time
		Connections            []p2p.ConnectionInfo
		NetworkStats           metrics.Stats
		NetworkStatsInIECUnits StatsInUnits
		Ping                   time.Duration `swaggertype:"primitive,integer"`
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
		VPN                     VPNInfo
		SOCKS5                  SOCKS5Info
		VPNGateway              VPNGatewayInfo
	}

	VPNInfo struct {
		VPNInterfaceEnabled bool
		InterfaceName       string
		IPNet               string
		IPv6Addr            string
	}

	SOCKS5Info struct {
		ListenAddress   string
		ProxyingEnabled bool
		ListenerEnabled bool
		// Connected — current libp2p connectivity to the exit peer.
		Connected             bool
		UsingPeerID           string
		UsingPeerName         string
		UsingPeerPublicIP     string
		UsingPeerPing         time.Duration `swaggertype:"primitive,integer"`
		UsingPeerThroughRelay bool
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
		// SuggestedIP is a free IP address generated for this peer
		SuggestedIP string
	}

	// InviteResponse describes one invite link. The token is never returned on
	// its own — only inside Link, which is what the user shares.
	InviteResponse struct {
		ID    string
		Label string
		// Link is rebuilt on every request from the current node name, so
		// renaming this node changes the link shown here while links already
		// handed out keep working — the name plays no part in validation.
		Link                 string
		Alias                string
		AllowUsingAsExitNode bool
		MaxUses              int
		UsedCount            int
		// ExpiresAt zero means the invite never expires.
		ExpiresAt time.Time
		CreatedAt time.Time
		Revoked   bool
		// Status is derived from the fields above for display.
		Status string `enums:"active,expired,used_up,revoked"`
	}

	ListAvailableProxiesResponse struct {
		Proxies []AvailableProxy
	}
	AvailableProxy struct {
		PeerID    string
		PeerName  string
		Connected bool
	}

	VPNGatewayInfo struct {
		// ClientEnabled — VPN gateway client mode is on (we route via GatewayPeerID).
		ClientEnabled bool
		// GatewayPeerID — the peer we route through; empty when disabled.
		GatewayPeerID string
		// GatewayPeerName — display name of the gateway peer, populated when known.
		GatewayPeerName string
		// Connected — current libp2p connectivity to the gateway peer.
		Connected bool
		// ServerEnabled — this node currently offers VPN gateway server.
		ServerEnabled       bool
		GatewayPublicIP     string
		GatewayPing         time.Duration `swaggertype:"primitive,integer"`
		GatewayThroughRelay bool
	}
	EnableVPNGatewayClientRequest struct {
		GatewayPeerID string `validate:"required"`
	}

	SetVPNGatewayServerEnabledRequest struct {
		Enabled bool
	}

	ListAvailableVPNGatewaysResponse struct {
		VPNGateways []AvailableVPNGateway
	}
	AvailableVPNGateway struct {
		PeerID    string
		PeerName  string
		Connected bool
	}
)

// Values of InviteResponse.Status.
const (
	InviteStatusActive  = "active"
	InviteStatusExpired = "expired"
	InviteStatusUsedUp  = "used_up"
	InviteStatusRevoked = "revoked"
)

type (
	P2pDebugInfo struct {
		General     GeneralDebugInfo
		DHT         DhtDebugInfo
		Connections ConnectionsDebugInfo
		Bandwidth   BandwidthDebugInfo
		KnownPeers  []KnownPeersResponse
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
		ReachableAddrs      []string
		UnreachableAddrs    []string
		UnknownAddrs        []string
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
