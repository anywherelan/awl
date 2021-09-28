package config

import (
	"encoding/json"
	"io/ioutil"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/anywherelan/awl/awlevent"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/mr-tron/base58/base58"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap/zapcore"
)

const (
	AppConfigFilename         = "config_awl.json"
	AppDataDirectory          = "anywherelan"
	DhtPeerstoreDataDirectory = "peerstore"
	AppDataDirEnvKey          = "AWL_DATA_DIR"

	// TODO 8989 maybe?
	DefaultHTTPPort      = 8639
	HttpServerDomainName = "admin"
)

type (
	Config struct {
		sync.RWMutex `swaggerignore:"true"`
		dataDir      string
		emitter      awlevent.Emitter

		Version           string               `json:"version"`
		LoggerLevel       string               `json:"loggerLevel"`
		HttpListenAddress string               `json:"httpListenAddress"`
		P2pNode           P2pNodeConfig        `json:"p2pNode"`
		VPNConfig         VPNConfig            `json:"vpn"`
		KnownPeers        map[string]KnownPeer `json:"knownPeers"`
	}
	P2pNodeConfig struct {
		// Hex-encoded multihash representing a peer ID, calculated from Identity
		PeerID                  string        `json:"peerId"`
		Name                    string        `json:"name"`
		Identity                string        `json:"identity"`
		BootstrapPeers          []string      `json:"bootstrapPeers"`
		ListenAddresses         []string      `json:"listenAddresses"`
		ReconnectionIntervalSec time.Duration `json:"reconnectionIntervalSec" swaggertype:"primitive,integer"`
	}
	VPNConfig struct {
		InterfaceName string `json:"interfaceName"`
		IPNet         string `json:"ipNet"`
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
	}
)

func (c *Config) Save() {
	c.RLock()
	c.save()
	c.RUnlock()
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

func (c *Config) RemovePeer(peerID string) bool {
	c.Lock()
	_, exists := c.KnownPeers[peerID]
	if exists {
		delete(c.KnownPeers, peerID)
		c.save()
	}
	c.Unlock()

	if exists {
		c.emitter.Emit(awlevent.KnownPeerChanged{})
	}

	return exists
}

func (c *Config) UpsertPeer(peer KnownPeer) {
	c.Lock()
	c.KnownPeers[peer.PeerID] = peer
	c.save()
	c.Unlock()

	c.emitter.Emit(awlevent.KnownPeerChanged{})
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

func (c *Config) SetIdentity(key crypto.PrivKey, id peer.ID) {
	c.Lock()
	by, _ := key.Raw()
	identity := base58.Encode(by)

	c.P2pNode.Identity = identity
	c.P2pNode.PeerID = id.Pretty()
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

func (c *Config) GetBootstrapPeers() []multiaddr.Multiaddr {
	c.RLock()
	allMultiaddrs := make([]multiaddr.Multiaddr, 0, len(c.P2pNode.BootstrapPeers))
	for _, val := range c.P2pNode.BootstrapPeers {
		newMultiaddr, _ := multiaddr.NewMultiaddr(val)
		allMultiaddrs = append(allMultiaddrs, newMultiaddr)
	}
	c.RUnlock()

	allMultiaddrs = append(allMultiaddrs, DefaultBootstrapPeers...)

	result := make([]multiaddr.Multiaddr, 0, len(allMultiaddrs))
	resultMap := make(map[string]struct{}, len(allMultiaddrs))
	for _, maddr := range allMultiaddrs {
		if _, exists := resultMap[maddr.String()]; !exists {
			resultMap[maddr.String()] = struct{}{}
			result = append(result, maddr)
		}
	}
	return result
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
		// TODO: check err
		newMultiaddr, _ := multiaddr.NewMultiaddr(val)
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
	err = ioutil.WriteFile(path, data, filesPerm)
	if err != nil {
		logger.DPanicf("Save config: %v", err)
	}
}

func (c *Config) path() string {
	path := filepath.Join(c.dataDir, AppConfigFilename)
	return path
}

func (kp KnownPeer) PeerId() peer.ID {
	peerID, err := peer.Decode(kp.PeerID)
	if err != nil {
		logger.DPanicf("Invalid hex-encoded multihash representing of a peer ID '%s': %v", kp.PeerID, err)
	}
	return peerID
}

func (kp *KnownPeer) DisplayName() string {
	name := kp.Name
	if kp.Alias != "" {
		name = kp.Alias
	}

	return name
}
