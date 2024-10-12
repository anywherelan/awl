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

		Version               string                 `json:"version"`
		LoggerLevel           string                 `json:"loggerLevel"`
		HttpListenAddress     string                 `json:"httpListenAddress"`
		HttpListenOnAdminHost bool                   `json:"httpListenOnAdminHost"`
		P2pNode               P2pNodeConfig          `json:"p2pNode"`
		VPNConfig             VPNConfig              `json:"vpn"`
		SOCKS5                SOCKS5Config           `json:"socks5"`
		KnownPeers            map[string]KnownPeer   `json:"knownPeers"`
		BlockedPeers          map[string]BlockedPeer `json:"blockedPeers"`
		Update                UpdateConfig           `json:"update"`
	}
	P2pNodeConfig struct {
		// Hex-encoded multihash representing a peer ID, calculated from Identity
		PeerID                  string        `json:"peerId"`
		Name                    string        `json:"name"`
		Identity                string        `json:"identity"`
		BootstrapPeers          []string      `json:"bootstrapPeers"`
		ListenAddresses         []string      `json:"listenAddresses"`
		ReconnectionIntervalSec time.Duration `json:"reconnectionIntervalSec" swaggertype:"primitive,integer"`
		AutoAcceptAuthRequests  bool          `json:"autoAcceptAuthRequests"`

		UseDedicatedConnForEachStream bool `json:"useDedicatedConnForEachStream"`
		ParallelSendingStreamsCount   int  `json:"parallelSendingStreamsCount"`
	}
	VPNConfig struct {
		InterfaceName string `json:"interfaceName"`
		IPNet         string `json:"ipNet"`
	}
	SOCKS5Config struct {
		ListenerEnabled bool `json:"listenerEnabled"`
		// allow using my host as proxy
		ProxyingEnabled bool   `json:"proxyingEnabled"`
		ListenAddress   string `json:"listenAddress"`
		// peer that is set as proxy
		UsingPeerID string `json:"usingPeerID"`
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
)

func (c *Config) Save() {
	c.RLock()
	c.save()
	c.RUnlock()
}

func (c *Config) IsUniqPeerAlias(excludePeerID, alias string) bool {
	c.RLock()
	defer c.RUnlock()
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
	knownPeer, ok := c.KnownPeers[peerID]
	c.RUnlock()
	return knownPeer, ok
}

func (c *Config) RemovePeer(peerID string) (KnownPeer, bool) {
	c.Lock()
	knownPeer, exists := c.KnownPeers[peerID]
	if exists {
		delete(c.KnownPeers, peerID)
		c.save()
	}
	c.Unlock()

	if exists {
		_ = c.emitter.Emit(awlevent.KnownPeerChanged{})
	}

	return knownPeer, exists
}

func (c *Config) UpsertPeer(peer KnownPeer) {
	c.Lock()
	c.KnownPeers[peer.PeerID] = peer
	c.save()
	c.Unlock()

	_ = c.emitter.Emit(awlevent.KnownPeerChanged{})
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
		c.save()
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
	c.save()
	c.Unlock()
}

func (c *Config) SetIdentity(key crypto.PrivKey, id peer.ID) {
	c.Lock()
	by, _ := key.Raw()
	identity := base58.Encode(by)

	c.P2pNode.Identity = identity
	c.P2pNode.PeerID = id.String()
	c.save()
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
	c.RUnlock()

	allMultiaddrs = append(allMultiaddrs, DefaultBootstrapPeers...)
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

func (c *Config) VPNLocalIPMask() (net.IP, net.IPMask) {
	localIP, ipNet, err := net.ParseCIDR(c.VPNConfig.IPNet)
	if err != nil {
		logger.Errorf("parse CIDR %s: %v", c.VPNConfig.IPNet, err)
		return nil, nil
	}
	return localIP.To4(), ipNet.Mask
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

func (c *Config) save() {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		logger.DPanicf("Marshal config: %v", err)
		return
	}
	path := c.path()
	err = os.WriteFile(path, data, filesPerm)
	if err != nil {
		logger.DPanicf("Save config: %v", err)
	}
	ChownFileIfNeeded(path)
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
