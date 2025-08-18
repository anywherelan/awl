package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/multiformats/go-multiaddr"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/awlevent"
)

const (
	filesPerm = 0600
	dirsPerm  = 0700
)

// TODO: move to Config struct?
var logger = log.Logger("awl/config")

var DefaultBootstrapPeers []multiaddr.Multiaddr

func init() {
	for _, s := range []string{
		"/dnsaddr/rus-1.bootstrap.anywherelan.com/p2p/12D3KooWNWa2r6dJVogbjNf1CKrKNttVAhKZr1PpWRPJYX7o4t4M",
		"/dnsaddr/rus-2.bootstrap.anywherelan.com/p2p/12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ",
		"/dnsaddr/ita-1.bootstrap.anywherelan.com/p2p/12D3KooWRXyTH7ZxerZRu6UtYQx62uCmYeZ244SsLQZbjuxX7RrL",
		"/dnsaddr/cze-1.bootstrap.anywherelan.com/p2p/12D3KooWJDDYCWbLYyCLTH16TFBZoxyDYD1Ypth2rtyznXYpnpza",
		"/dnsaddr/can-1.bootstrap.anywherelan.com/p2p/12D3KooWQeAvoyVnRm6T5XzWpKD8AzM1buzBL6o95iCodCZVQAsV",

		// copy of cze-1 in case dns does not work
		"/ip4/195.181.214.203/tcp/6150/p2p/12D3KooWJDDYCWbLYyCLTH16TFBZoxyDYD1Ypth2rtyznXYpnpza",
		"/ip4/195.181.214.203/udp/6150/quic-v1/p2p/12D3KooWJDDYCWbLYyCLTH16TFBZoxyDYD1Ypth2rtyznXYpnpza",

		// community relay server from pftmclub - location at Vietnam - Hosting Provided by Vietnix VPS
		"/ip4/222.255.238.138/tcp/9500/p2p/12D3KooWPfMmyZT1GAP7cDFSurPhAfKX3QFktK9rDLxD5zDZD6HW",
		"/ip4/222.255.238.138/udp/9500/quic-v1/p2p/12D3KooWPfMmyZT1GAP7cDFSurPhAfKX3QFktK9rDLxD5zDZD6HW",
	} {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			logger.DPanicf("parse multiaddr: %v", err)
			continue
		}
		DefaultBootstrapPeers = append(DefaultBootstrapPeers, ma)
	}
}

func CalcAppDataDir() string {
	if envDir := os.Getenv(AppDataDirEnvKey); envDir != "" {
		err := os.MkdirAll(envDir, dirsPerm)
		if err != nil {
			logger.Errorf("could not create data directory from env: %v", err)
		}
		ChownFileIfNeeded(envDir)
		return envDir
	}

	var executableDir string
	ex, err := os.Executable()
	if err != nil {
		logger.Errorf("find executable path: %v", err)
	} else {
		executableDir = filepath.Dir(ex)
	}
	if executableDir != "" {
		configPath := filepath.Join(executableDir, AppConfigFilename)
		if _, err := os.Stat(configPath); err == nil {
			return executableDir
		}
	}

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		logger.Warnf("could not get user config directory: %v", err)
		return ""
	}
	userDataDir := filepath.Join(userConfigDir, AppDataDirectory)
	err = os.MkdirAll(userDataDir, dirsPerm)
	if err != nil {
		logger.Warnf("could not create data directory in user dir: %v", err)
		return ""
	}
	ChownFileIfNeeded(userDataDir)

	return userDataDir
}

func NewConfig(bus awlevent.Bus) *Config {
	conf := &Config{}
	setDefaults(conf, bus)
	return conf
}

func LoadConfig(bus awlevent.Bus) (*Config, error) {
	dataDir := CalcAppDataDir()
	configPath := filepath.Join(dataDir, AppConfigFilename)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	// TODO: config migration
	conf := new(Config)
	err = json.Unmarshal(data, conf)
	if err != nil {
		return nil, err
	}
	conf.dataDir = dataDir
	setDefaults(conf, bus)
	return conf, nil
}

func ImportConfig(data []byte, directory string) error {
	conf := new(Config)
	err := json.Unmarshal(data, conf)
	if err != nil {
		return fmt.Errorf("invalid format: %v", err)
	}

	path := filepath.Join(directory, AppConfigFilename)
	err = os.WriteFile(path, data, filesPerm)
	if err != nil {
		return fmt.Errorf("save file: %v", err)
	}

	logger.Infof("Imported new config to '%s'", path)
	return nil
}

func setDefaults(conf *Config, bus awlevent.Bus) {
	isEmptyConfig := conf.Version == ""

	conf.Version = Version

	// P2pNode
	if conf.P2pNode.ListenAddresses == nil {
		conf.P2pNode.ListenAddresses = make([]string, 0)
	}
	if conf.P2pNode.BootstrapPeers == nil {
		conf.P2pNode.BootstrapPeers = make([]string, 0)
	}
	if conf.P2pNode.ReconnectionIntervalSec == 0 {
		conf.P2pNode.ReconnectionIntervalSec = 20
	}
	if conf.P2pNode.ParallelSendingStreamsCount == 0 {
		conf.P2pNode.ParallelSendingStreamsCount = 1
	}

	// Other
	if conf.LoggerLevel == "" {
		conf.LoggerLevel = "info"
	}
	if conf.HttpListenAddress == "" {
		conf.HttpListenAddress = "127.0.0.1:" + strconv.Itoa(DefaultHTTPPort)
	}
	if isEmptyConfig {
		conf.HttpListenOnAdminHost = true
	}

	if conf.VPNConfig.IPNet == "" {
		conf.VPNConfig.IPNet = defaultNetworkSubnet
	}
	if ip, _ := conf.VPNLocalIPMask(); ip == nil {
		conf.VPNConfig.IPNet = defaultNetworkSubnet
	}
	if conf.VPNConfig.InterfaceName == "" {
		if runtime.GOOS == "darwin" {
			conf.VPNConfig.InterfaceName = "utun"
		} else {
			conf.VPNConfig.InterfaceName = defaultInterfaceName
		}
	}

	if conf.SOCKS5 == (SOCKS5Config{}) {
		conf.SOCKS5.ListenerEnabled = true
		conf.SOCKS5.ProxyingEnabled = true
	}
	if conf.SOCKS5.ListenAddress == "" {
		conf.SOCKS5.ListenAddress = defaultSOCKS5ListenAddress
	}

	uniqAliases := make(map[string]struct{}, len(conf.KnownPeers))
	if conf.KnownPeers == nil {
		conf.KnownPeers = make(map[string]KnownPeer)
	}
	for peerID := range conf.KnownPeers {
		peer := conf.KnownPeers[peerID]
		newAlias := conf.genUniqPeerAlias(peer.Name, peer.Alias, uniqAliases)
		if newAlias != peer.Alias {
			logger.Warnf("incorrect config: peer (id: %s) alias %s is not unique, updated automaticaly to %s", peerID, peer.Alias, newAlias)
			peer.Alias = newAlias
		}
		if peer.IPAddr == "" {
			peer.IPAddr = conf.GenerateNextIpAddr()
		}
		if peer.DomainName == "" {
			peer.DomainName = awldns.TrimDomainName(peer.DisplayName())
		}
		conf.KnownPeers[peerID] = peer
	}

	if conf.BlockedPeers == nil {
		conf.BlockedPeers = make(map[string]BlockedPeer)
	}

	if conf.dataDir == "" {
		conf.dataDir = CalcAppDataDir()
	}

	// Create dirs
	// TODO: currently PeerstoreDataDir is not used
	// peerstoreDir := filepath.Join(conf.dataDir, DhtPeerstoreDataDirectory)
	// err := os.MkdirAll(peerstoreDir, dirsPerm)
	// if err != nil {
	//	logger.Warnf("could not create peerstore directory: %v", err)
	// }
	// ChownFileIfNeeded(peerstoreDir)

	emitter, err := bus.Emitter(new(awlevent.KnownPeerChanged), eventbus.Stateful)
	if err != nil {
		panic(err)
	}
	conf.emitter = emitter

	if u := conf.Update.UpdateServerURL; u == "" || u == "http://example/example.json" {
		conf.Update.UpdateServerURL = "https://build.anywherelan.com/repository/releases.json"
	} else {
		if _, err := url.Parse(conf.Update.UpdateServerURL); err != nil {
			logger.Warnf("incorrect update server url. err:%v", err)
		}
	}
	if !conf.Update.TrayAutoCheckEnabled && conf.Update.TrayAutoCheckInterval == "" {
		conf.Update.TrayAutoCheckEnabled = true
	}
	if i := conf.Update.TrayAutoCheckInterval; i == "" || i == "24h" {
		conf.Update.TrayAutoCheckInterval = "8h"
	}
}

func ChownFileIfNeeded(path string) {
	if runtime.GOOS != "linux" || LinuxFilesOwnerUID == 0 {
		return
	}
	err := os.Chown(path, LinuxFilesOwnerUID, LinuxFilesOwnerUID)
	if err != nil {
		logger.Errorf("Chown file '%s': %v", path, err)
	}
}
