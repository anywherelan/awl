package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/mr-tron/base58/base58"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap/zapcore"

	"github.com/anywherelan/awl/awlevent"
)

const (
	AppConfigFilename         = "config_awl.json"
	AppDataDirectory          = "anywherelan"
	DhtPeerstoreDataDirectory = "peerstore"
	AppDataDirEnvKey          = "AWL_DATA_DIR"

	// TODO 8989 maybe?
	DefaultHTTPPort              = 8639
	AdminHttpServerDomainName    = "admin"
	AdminHttpServerIP            = "127.0.0.66"
	AdminHttpServerListenAddress = "127.0.0.66:80"

	defaultSOCKS5ListenAddress = "127.0.0.66:8080"

	DefaultPeerAlias = "peer"
)

// LinuxFilesOwnerUID is used to set correct files owner uid.
// This is needed because by default all files belong to root when we run as root, but they are stored in user's directory.
var LinuxFilesOwnerUID = os.Geteuid()

type (
	Config struct {
		sync.RWMutex `swaggerignore:"true"`
		dataDir      string
		emitter      awlevent.Emitter
		appType      AppType
		// netstackDNSIP is the in-subnet IP reserved for the awl DNS server:
		// broadcast−1 shifted down past taken addresses. Computed once in
		// setDefaults from the config snapshot (not serialized), fixed for the
		// session. Reserved on every platform so it is never handed to a peer
		// (see CheckIPUnique), though only the Android netstack DNS interceptor
		// (awldns/dnsbridge) actually serves on it today; kept reserved on
		// desktop for a future userspace-netstack path.
		netstackDNSIP net.IP

		// saveTrigger carries save requests to the writer goroutine. Capacity
		// one, so a request that finds it full is simply dropped: a write is
		// already queued and marshals at write time, which is what makes a
		// burst of saves collapse into a single write of the newest state.
		//
		// nil on a config from NewConfigReadOnly/LoadConfigReadOnly, which has no writer.
		saveTrigger chan struct{}
		closeCh     chan struct{}
		writerDone  chan struct{}
		closeOnce   sync.Once

		Version               string                 `json:"version"`
		LoggerLevel           string                 `json:"loggerLevel"`
		HttpListenAddress     string                 `json:"httpListenAddress"`
		HttpListenOnAdminHost bool                   `json:"httpListenOnAdminHost"`
		HttpBasicAuth         HttpBasicAuthConfig    `json:"httpBasicAuth"`
		P2pNode               P2pNodeConfig          `json:"p2pNode"`
		VPNConfig             VPNConfig              `json:"vpn"`
		VPNGateway            VPNGatewayConfig       `json:"vpnGateway"`
		SOCKS5                SOCKS5Config           `json:"socks5"`
		DNS                   DNSConfig              `json:"dns"`
		KnownPeers            map[string]KnownPeer   `json:"knownPeers"`
		BlockedPeers          map[string]BlockedPeer `json:"blockedPeers"`
		Invites               map[string]Invite      `json:"invites"`
		Update                UpdateConfig           `json:"update"`
	}
	P2pNodeConfig struct {
		// Hex-encoded multihash representing a peer ID, calculated from Identity
		PeerID         string   `json:"peerId"`
		Name           string   `json:"name"`
		Identity       string   `json:"identity"`
		BootstrapPeers []string `json:"bootstrapPeers"`
		// With this option only BootstrapPeers from config will be used
		IgnoreDefaultBootstrapPeers *bool         `json:"ignoreDefaultBootstrapPeers,omitempty"`
		ListenAddresses             []string      `json:"listenAddresses"`
		ReconnectionIntervalSec     time.Duration `json:"reconnectionIntervalSec" swaggertype:"primitive,integer"` //nolint:staticcheck
		AutoAcceptAuthRequests      bool          `json:"autoAcceptAuthRequests"`

		UseDedicatedConnForEachStream bool `json:"useDedicatedConnForEachStream"`
		ParallelSendingStreamsCount   int  `json:"parallelSendingStreamsCount"`
	}
	VPNConfig struct {
		DisableVPNInterface bool   `json:"disableVPNInterface"`
		InterfaceName       string `json:"interfaceName"`
		IPNet               string `json:"ipNet"`
		IPNetV6             string `json:"ipNetV6"`
	}
	// VPNGatewayConfig configures full-tunnel VPN gateway mode.
	//
	// VPN gateway is a separate feature from SOCKS5 exit-node: a peer can
	// allow being used as a SOCKS5 exit (KnownPeer.AllowedUsingAsExitNode,
	// propagated via the status protocol) without serving as a VPN gateway,
	// and vice versa. KnownPeer.CanUseAsVPNGateway() combines both.
	VPNGatewayConfig struct {
		// ClientEnabled — route all traffic through GatewayPeerID (client side).
		ClientEnabled bool `json:"clientEnabled"`
		// GatewayPeerID — selected gateway peer ID.
		GatewayPeerID string `json:"gatewayPeerID"`
		// ServerEnabled — this node serves as a VPN gateway for others.
		// Propagated via the status protocol so peers know whether to offer
		// this node as an option in their UI.
		ServerEnabled bool `json:"serverEnabled"`
	}
	SOCKS5Config struct {
		ListenerEnabled bool `json:"listenerEnabled"`
		// allow using my host as proxy
		ProxyingEnabled bool   `json:"proxyingEnabled"`
		ListenAddress   string `json:"listenAddress"`
		// peer that is set as proxy
		UsingPeerID string `json:"usingPeerID"`
		// Optional local auth credentials. If both are set, SOCKS5 clients must authenticate.
		Username string `json:"username"`
		Password string `json:"password"`
	}
	DNSConfig struct {
		DisableDNS    bool   `json:"disableDNS"`
		ListenAddress string `json:"listenAddress"`
		// UpstreamDNSAddress is the public resolver (host:port) that the awl
		// DNS resolver forwards non-.awl queries to. Used as a fallback when
		// the OS exposes no base nameserver, and forced as the sole upstream in
		// VPN gateway client mode so DNS traverses the tunnel and does not leak.
		// On Android the host reads this value to configure VpnService DNS.
		UpstreamDNSAddress string `json:"upstreamDNSAddress"`
	}
	KnownPeer struct {
		// Hex-encoded multihash representing a peer ID
		PeerID string `json:"peerId"`
		// Peer provided name
		Name string `json:"name"`
		// User provided name
		Alias string `json:"alias"`
		// IPAddr used for forwarding
		IPAddr string `json:"ipAddr"`
		// IPAddrV6 used for IPv6 overlay forwarding (derived from peerID)
		IPAddrV6 string `json:"ipAddrV6"`
		// DomainName without zone suffix (.awl)
		DomainName string `json:"domainName"`
		// Time of adding to config (accept/invite)
		CreatedAt time.Time `json:"createdAt"`
		// Time of last connection
		LastSeen time.Time `json:"lastSeen"`
		// Has remote peer confirmed our invitation
		Confirmed bool `json:"confirmed"`
		// Has remote peer declined our invitation
		Declined               bool `json:"declined"`
		WeAllowUsingAsExitNode bool `json:"weAllowUsingAsExitNode"`
		AllowedUsingAsExitNode bool `json:"allowedUsingAsExitNode"`
		// RemoteVPNGatewayServerEnabled is the remote peer's VPNGatewayConfig.ServerEnabled
		// as advertised via the status protocol. Combined with AllowedUsingAsExitNode
		// (also from status) it determines whether this peer is currently a valid
		// VPN gateway target for us — see KnownPeer.CanUseAsVPNGateway.
		RemoteVPNGatewayServerEnabled bool `json:"remoteVPNGatewayServerEnabled"`
		// InviteID marks a peer we let in through one of our invite links
		// (Config.Invites). Non-secret, kept forever: it is what lets the UI say
		// "added via invite X".
		InviteID string `json:"inviteID,omitempty"`
		// PendingInviteToken is the secret from an invite link we are redeeming:
		// we present it in every auth request until the peer confirms us, then it
		// is dropped. Set only on the side that used the link.
		PendingInviteToken string `json:"pendingInviteToken,omitempty"`
	}
	// Invite is a link we handed out: whoever presents Token is added
	// automatically, with the settings stored here, until the invite is used up,
	// expires or is revoked. Invites are kept forever, including spent ones, as
	// history and as the target of KnownPeer.InviteID.
	Invite struct {
		// ID is a short non-secret identifier, the key in Config.Invites. UI, CLI
		// and logs refer to an invite by it so the token stays out of sight.
		ID string `json:"id"`
		// Token is the bearer secret embedded in the link. Stored in the clear
		// because the link has to be displayable again.
		Token string `json:"token"`
		// Label is an optional human-readable note.
		Label string `json:"label,omitempty"`
		// Alias to give the new peer; single-use invites only, aliases are unique.
		Alias string `json:"alias,omitempty"`
		// WeAllowUsingAsExitNode is applied to the new peer's KnownPeer.
		WeAllowUsingAsExitNode bool `json:"weAllowUsingAsExitNode"`
		MaxUses                int  `json:"maxUses"`
		UsedCount              int  `json:"usedCount"`
		// ExpiresAt is checked against our own clock at redemption time; zero
		// means the invite never expires.
		ExpiresAt time.Time `json:"expiresAt"`
		CreatedAt time.Time `json:"createdAt"`
		Revoked   bool      `json:"revoked"`
	}
	BlockedPeer struct {
		// Hex-encoded multihash representing a peer ID
		PeerID      string `json:"peerId"`
		DisplayName string `json:"displayName"`
		// Time of adding to config (decline invitation/remove from KnownPeers)
		CreatedAt time.Time `json:"createdAt"`
	}
	UpdateConfig struct {
		LowestPriorityChan    string `json:"lowestPriorityChan"`
		UpdateServerURL       string `json:"updateServerURL"`
		TrayAutoCheckEnabled  bool   `json:"trayAutoCheckEnabled"`
		TrayAutoCheckInterval string `json:"trayAutoCheckInterval"`
	}
	HttpBasicAuthConfig struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
)

// Save schedules a write of the config and returns immediately; the writer
// goroutine performs it. It is safe to call with or without the config lock
// held, because it touches no config state — only saveTrigger.
func (c *Config) Save() {
	if c.saveTrigger == nil {
		// A read-only config has no writer. Reaching here means something
		// mutated a snapshot and expected it to persist.
		logger.DPanicf("Save called on a read-only config")
		return
	}

	select {
	case c.saveTrigger <- struct{}{}:
	default:
		// a write is already queued and will pick up the newest state
	}
}

func (c *Config) IsUniqPeerAlias(excludePeerID, alias string) bool {
	c.RLock()
	defer c.RUnlock()
	return c.IsUniqPeerAliasUnlocked(excludePeerID, alias)
}

// IsUniqPeerAliasUnlocked is IsUniqPeerAlias without locking; the caller must hold the lock.
func (c *Config) IsUniqPeerAliasUnlocked(excludePeerID, alias string) bool {
	for _, kPeer := range c.KnownPeers {
		if kPeer.PeerID == excludePeerID {
			continue
		}
		if kPeer.Alias == alias {
			return false
		}
	}
	return true
}

func (c *Config) GenUniqPeerAlias(name, alias string) string {
	c.RLock()
	alias = c.genUniqPeerAlias(name, alias, nil)
	c.RUnlock()
	return alias
}

// GenUniqPeerAliasUnlocked is GenUniqPeerAlias without locking; the caller must hold the lock.
func (c *Config) GenUniqPeerAliasUnlocked(name, alias string) string {
	return c.genUniqPeerAlias(name, alias, nil)
}

func (c *Config) KnownPeersIds() []peer.ID {
	c.RLock()
	ids := make([]peer.ID, 0, len(c.KnownPeers))
	for _, known := range c.KnownPeers {
		ids = append(ids, known.PeerId())
	}
	c.RUnlock()
	return ids
}

func (c *Config) GetPeer(peerID string) (KnownPeer, bool) {
	c.RLock()
	knownPeer, ok := c.GetPeerUnlocked(peerID)
	c.RUnlock()
	return knownPeer, ok
}

// GetPeerUnlocked is GetPeer without locking; the caller must hold the lock.
func (c *Config) GetPeerUnlocked(peerID string) (KnownPeer, bool) {
	knownPeer, ok := c.KnownPeers[peerID]
	return knownPeer, ok
}

// NodeName returns this node's name under the read lock. P2pNode.Name is mutated
// at runtime (UpdateMySettings), so it must not be read directly without the lock.
func (c *Config) NodeName() string {
	c.RLock()
	defer c.RUnlock()
	return c.P2pNode.Name
}

func (c *Config) RemovePeer(peerID string) (KnownPeer, bool) {
	c.Lock()
	knownPeer, exists := c.KnownPeers[peerID]
	if exists {
		delete(c.KnownPeers, peerID)
		c.Save()
	}
	c.Unlock()

	if exists {
		c.emitKnownPeerChanged()
	}

	return knownPeer, exists
}

func (c *Config) UpsertPeer(peer KnownPeer) {
	c.Lock()
	c.KnownPeers[peer.PeerID] = peer
	c.Save()
	c.Unlock()

	c.emitKnownPeerChanged()
}

func (c *Config) UpsertPeerUnlocked(peer KnownPeer) {
	c.KnownPeers[peer.PeerID] = peer
	c.Save()

	c.emitKnownPeerChanged()
}

// UpdatePeerFields atomically applies mutate to the stored KnownPeer under the
// write lock. It returns false if the peer is unknown.
//
// mutate must only change the fields it owns and must NOT replace the struct
// wholesale, so that fields updated concurrently by other callers are not
// clobbered. mutate runs while the lock is held, so it must not call other
// Config methods that take the lock (use the *Unlocked variants instead).
func (c *Config) UpdatePeerFields(peerID string, mutate func(*KnownPeer)) bool {
	c.Lock()
	knownPeer, ok := c.KnownPeers[peerID]
	changed := false
	if ok {
		updated := knownPeer
		mutate(&updated)
		c.KnownPeers[peerID] = updated

		changed = peerChangedIgnoringLastSeen(knownPeer, updated)
		if changed {
			c.Save()
		}
	}
	c.Unlock()

	if changed {
		c.emitKnownPeerChanged()
	}
	return ok
}

// peerChangedIgnoringLastSeen reports whether anything except LastSeen differs.
func peerChangedIgnoringLastSeen(before, after KnownPeer) bool {
	before.LastSeen = time.Time{}
	after.LastSeen = time.Time{}
	return before != after
}

func (c *Config) UpdatePeerLastSeen(peerID string) {
	c.Lock()
	knownPeer, ok := c.KnownPeers[peerID]
	if ok {
		knownPeer.LastSeen = time.Now()
		c.KnownPeers[peerID] = knownPeer
	}
	c.Unlock()
}

func (c *Config) GetBlockedPeer(peerID string) (BlockedPeer, bool) {
	c.RLock()
	blockedPeer, ok := c.BlockedPeers[peerID]
	c.RUnlock()
	return blockedPeer, ok
}

func (c *Config) RemoveBlockedPeer(peerID string) {
	c.Lock()
	_, exists := c.BlockedPeers[peerID]
	if exists {
		delete(c.BlockedPeers, peerID)
		c.Save()
	}
	c.Unlock()
}

func (c *Config) UpsertBlockedPeer(peerID, displayName string) {
	c.Lock()
	blockedPeer, exists := c.BlockedPeers[peerID]
	if !exists {
		blockedPeer.CreatedAt = time.Now()
	}
	blockedPeer.PeerID = peerID
	blockedPeer.DisplayName = displayName
	c.BlockedPeers[peerID] = blockedPeer
	c.Save()
	c.Unlock()
}

func (c *Config) SetIdentity(key crypto.PrivKey, id peer.ID) {
	c.Lock()
	by, _ := key.Raw()
	identity := base58.Encode(by)

	c.P2pNode.Identity = identity
	c.P2pNode.PeerID = id.String()
	c.ensureIPv6AddressLocked()
	c.Save()
	c.Unlock()
}

func (c *Config) PrivKey() []byte {
	c.RLock()
	defer c.RUnlock()

	if c.P2pNode.Identity == "" {
		return nil
	}
	b, err := base58.Decode(c.P2pNode.Identity)
	if err != nil {
		return nil
	}
	return b
}

func (c *Config) GetBootstrapPeers() []peer.AddrInfo {
	c.RLock()
	allMultiaddrs := make([]multiaddr.Multiaddr, 0, len(c.P2pNode.BootstrapPeers))
	for _, val := range c.P2pNode.BootstrapPeers {
		newMultiaddr, err := multiaddr.NewMultiaddr(val)
		if err != nil {
			logger.Warnf("invalid bootstrap multiaddr from config: %v", err)
			continue
		}

		allMultiaddrs = append(allMultiaddrs, newMultiaddr)
	}
	ignoreDefaultBootstrapPeers := c.P2pNode.IgnoreDefaultBootstrapPeers != nil && *c.P2pNode.IgnoreDefaultBootstrapPeers
	c.RUnlock()

	if !ignoreDefaultBootstrapPeers {
		allMultiaddrs = append(allMultiaddrs, DefaultBootstrapPeers...)
	}

	addrInfos, err := peer.AddrInfosFromP2pAddrs(allMultiaddrs...)
	if err != nil {
		logger.Warnf("invalid one or more bootstrap addr info from config: %v", err)
		addrInfos, err = peer.AddrInfosFromP2pAddrs(DefaultBootstrapPeers...)
		if err != nil {
			panic(err)
		}
	}

	return addrInfos
}

func (c *Config) SetListenAddresses(multiaddrs []multiaddr.Multiaddr) {
	c.Lock()
	result := make([]string, 0, len(multiaddrs))
	for _, val := range multiaddrs {
		result = append(result, val.String())
	}
	c.P2pNode.ListenAddresses = result
	c.Unlock()
}

func (c *Config) GetListenAddresses() []multiaddr.Multiaddr {
	c.RLock()
	result := make([]multiaddr.Multiaddr, 0, len(c.P2pNode.ListenAddresses))
	for _, val := range c.P2pNode.ListenAddresses {
		newMultiaddr, err := multiaddr.NewMultiaddr(val)
		if err != nil {
			logger.Errorf("parse listen address '%s': %v", val, err)
			continue
		}
		result = append(result, newMultiaddr)
	}
	c.RUnlock()
	return result
}

func (c *Config) DNSNamesMapping() map[string]string {
	mapping := make(map[string]string)
	c.RLock()
	defer c.RUnlock()

	for _, knownPeer := range c.KnownPeers {
		mapping[knownPeer.PeerID] = knownPeer.IPAddr
		if knownPeer.DomainName != "" {
			mapping[knownPeer.DomainName] = knownPeer.IPAddr
		}
	}

	return mapping
}

func (c *Config) DNSNamesMappingV6() map[string]string {
	mapping := make(map[string]string)
	c.RLock()
	defer c.RUnlock()

	for _, knownPeer := range c.KnownPeers {
		if knownPeer.IPAddrV6 == "" {
			continue
		}
		mapping[knownPeer.PeerID] = knownPeer.IPAddrV6
		if knownPeer.DomainName != "" {
			mapping[knownPeer.DomainName] = knownPeer.IPAddrV6
		}
	}

	return mapping
}

func (c *Config) PeerstoreDir() string {
	dir := filepath.Join(c.dataDir, DhtPeerstoreDataDirectory)
	return dir
}

func (c *Config) DataDir() string {
	return c.dataDir
}

func (c *Config) LogLevel() zapcore.Level {
	level := c.LoggerLevel
	if c.LoggerLevel == "dev" {
		level = "debug"
	}
	lvl := zapcore.InfoLevel
	_ = lvl.Set(level)
	return lvl
}

// DevMode
// Possible duplicate of IsDevVersion()
// Based on Config.LoggerLevel (could be used by any user)
func (c *Config) DevMode() bool {
	return c.LoggerLevel == "dev"
}

func (c *Config) Export() []byte {
	c.RLock()
	defer c.RUnlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		logger.DPanicf("Marshal config: %v", err)
	}
	return data
}

// Close stops the writer goroutine after a final flush and releases the event
// emitter. It is idempotent and safe on a read-only config, which has neither.
//
// Callers of NewConfig and LoadConfig must call it, otherwise the writer
// goroutine and the emitter's slot on the event bus outlive the config.
func (c *Config) Close() {
	c.closeOnce.Do(func() {
		if c.saveTrigger != nil {
			close(c.closeCh)
			<-c.writerDone
		}
		if c.emitter != nil {
			if err := c.emitter.Close(); err != nil {
				logger.Errorf("Close config emitter: %v", err)
			}
		}
	})
}

// startWriter launches the goroutine that owns writing this config to disk.
// Called by the constructors that hand out a config someone will mutate; the
// read-only ones skip it, which is what leaves saveTrigger nil.
func (c *Config) startWriter() {
	c.saveTrigger = make(chan struct{}, 1)
	c.closeCh = make(chan struct{})
	c.writerDone = make(chan struct{})

	go c.writer()
}

func (c *Config) writer() {
	defer close(c.writerDone)

	for {
		select {
		case <-c.saveTrigger:
			c.writeNow()
		case <-c.closeCh:
			// final flush
			c.writeNow()
			return
		}
	}
}

// writeNow is only ever called from the writer goroutine.
func (c *Config) writeNow() {
	c.RLock()
	data, err := json.MarshalIndent(c, "", "  ")
	c.RUnlock()
	if err != nil {
		// A marshal failure is a bug in the config structs, not an
		// environmental problem, so it stays a DPanic.
		logger.DPanicf("Marshal config: %v", err)
		return
	}

	if err = writeFileAtomic(c.path(), data); err != nil {
		logger.DPanicf("Save config: %v", err)
	}
}

// emitKnownPeerChanged announces a change to the known peers.
func (c *Config) emitKnownPeerChanged() {
	// The emitter is nil on a read-only config.
	if c.emitter == nil {
		return
	}
	_ = c.emitter.Emit(awlevent.KnownPeerChanged{})
}

func (c *Config) path() string {
	path := filepath.Join(c.dataDir, AppConfigFilename)
	return path
}

func (c *Config) genUniqPeerAlias(name, alias string, uniqAliases map[string]struct{}) string {
	if alias == "" {
		if name == "" {
			alias = DefaultPeerAlias
		} else {
			alias = name
		}
	}
	if uniqAliases == nil {
		uniqAliases = make(map[string]struct{}, len(c.KnownPeers)+1)
		for _, kPeer := range c.KnownPeers {
			uniqAliases[kPeer.Alias] = struct{}{}
		}
	}
	if _, ok := uniqAliases[alias]; ok {
		newAlias := ""
		for i := 0; ok; i++ {
			newAlias = fmt.Sprintf("%s_%d", alias, i)
			_, ok = uniqAliases[newAlias]
		}
		alias = newAlias
	}
	uniqAliases[alias] = struct{}{}
	return alias
}

func (kp KnownPeer) PeerId() peer.ID {
	peerID, err := peer.Decode(kp.PeerID)
	if err != nil {
		logger.DPanicf("Invalid hex-encoded multihash representing of a peer ID '%s': %v", kp.PeerID, err)
	}
	return peerID
}

func (kp KnownPeer) DisplayName() string {
	name := kp.Name
	if kp.Alias != "" {
		name = kp.Alias
	}

	return name
}

// CanUseAsVPNGateway reports whether this peer can currently be used as a
// VPN gateway. It requires both that the peer permits being used as an exit
// node (shared with SOCKS5 via AllowedUsingAsExitNode) and that the peer has
// VPN gateway server enabled on its side (RemoteVPNGatewayServerEnabled).
// Both flags are populated from the status protocol exchange in
// service/auth_status.go.
func (kp KnownPeer) CanUseAsVPNGateway() bool {
	return kp.AllowedUsingAsExitNode && kp.RemoteVPNGatewayServerEnabled
}
