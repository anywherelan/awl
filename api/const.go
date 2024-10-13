package api

const (
	V0Prefix = "/api/v0/"

	// Peers
	GetKnownPeersPath        = V0Prefix + "peers/get_known"
	GetKnownPeerSettingsPath = V0Prefix + "peers/get_known_peer_settings"
	UpdatePeerSettingsPath   = V0Prefix + "peers/update_settings"
	RemovePeerSettingsPath   = V0Prefix + "peers/remove"

	GetBlockedPeersPath = V0Prefix + "peers/get_blocked"

	SendFriendRequestPath    = V0Prefix + "peers/invite_peer"
	AcceptPeerInvitationPath = V0Prefix + "peers/accept_peer"
	GetAuthRequestsPath      = V0Prefix + "peers/auth_requests"

	// Settings
	GetMyPeerInfoPath        = V0Prefix + "settings/peer_info"
	UpdateMyInfoPath         = V0Prefix + "settings/update"
	ListAvailableProxiesPath = V0Prefix + "settings/list_proxies"
	UpdateProxySettingsPath  = V0Prefix + "settings/set_proxy"
	ExportServerConfigPath   = V0Prefix + "settings/export_server_config"

	// Debug
	GetP2pDebugInfoPath = V0Prefix + "debug/p2p_info"
	GetDebugLogPath     = V0Prefix + "debug/log"
)
