package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/awlevent"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-eventbus"
	"github.com/multiformats/go-multiaddr"
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
		"/ip4/45.67.230.153/tcp/6150/p2p/12D3KooWNWa2r6dJVogbjNf1CKrKNttVAhKZr1PpWRPJYX7o4t4M",
		"/ip4/45.67.230.153/udp/6150/quic/p2p/12D3KooWNWa2r6dJVogbjNf1CKrKNttVAhKZr1PpWRPJYX7o4t4M",

		"/ip4/45.67.230.223/tcp/6150/p2p/12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ",
		"/ip4/45.67.230.223/udp/6150/quic/p2p/12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ",

		"/ip4/212.237.53.149/tcp/6150/p2p/12D3KooWRXyTH7ZxerZRu6UtYQx62uCmYeZ244SsLQZbjuxX7RrL",
		"/ip4/212.237.53.149/udp/6150/quic/p2p/12D3KooWRXyTH7ZxerZRu6UtYQx62uCmYeZ244SsLQZbjuxX7RrL",
		"/ip6/2a00:6d40:72:d95::1/tcp/7250/p2p/12D3KooWRXyTH7ZxerZRu6UtYQx62uCmYeZ244SsLQZbjuxX7RrL",
		"/ip6/2a00:6d40:72:d95::1/udp/7250/quic/p2p/12D3KooWRXyTH7ZxerZRu6UtYQx62uCmYeZ244SsLQZbjuxX7RrL",

		"/ip4/195.181.214.203/tcp/6150/p2p/12D3KooWJDDYCWbLYyCLTH16TFBZoxyDYD1Ypth2rtyznXYpnpza",
		"/ip4/195.181.214.203/udp/6150/quic/p2p/12D3KooWJDDYCWbLYyCLTH16TFBZoxyDYD1Ypth2rtyznXYpnpza",
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
			logger.Warnf("could not create data directory from env: %v", err)
		}
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
	data, err := ioutil.ReadFile(configPath)
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
	err = ioutil.WriteFile(path, data, filesPerm)
	if err != nil {
		return fmt.Errorf("save file: %v", err)
	}

	logger.Infof("Imported new config to %s", path)
	return nil
}

func setDefaults(conf *Config, bus awlevent.Bus) {
	// P2pNode
	if len(conf.P2pNode.ListenAddresses) == 0 {
		var multiaddrs []multiaddr.Multiaddr
		for _, s := range []string{
			"/ip4/0.0.0.0/tcp/0",
			"/ip6/::/tcp/0",
			"/ip4/0.0.0.0/udp/0",
			"/ip4/0.0.0.0/udp/0/quic",
			"/ip6/::/udp/0",
			"/ip6/::/udp/0/quic",
		} {
			addr, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				logger.DPanicf("parse multiaddr: %v", err)
			}
			multiaddrs = append(multiaddrs, addr)
		}
		conf.SetListenAddresses(multiaddrs)
	}
	if conf.P2pNode.BootstrapPeers == nil {
		conf.P2pNode.BootstrapPeers = make([]string, 0)
	}
	if conf.P2pNode.ReconnectionIntervalSec == 0 {
		conf.P2pNode.ReconnectionIntervalSec = 20
	}

	// Other
	if conf.LoggerLevel == "" {
		conf.LoggerLevel = "info"
	}
	if conf.HttpListenAddress == "" {
		conf.HttpListenAddress = "127.0.0.1:" + strconv.Itoa(DefaultHTTPPort)
	}
	conf.Version = Version

	if conf.VPNConfig.IPNet == "" {
		conf.VPNConfig.IPNet = defaultNetworkSubnet
	}
	if ip, _ := conf.VPNLocalIPMask(); ip == nil {
		conf.VPNConfig.IPNet = defaultNetworkSubnet
	}
	if conf.VPNConfig.InterfaceName == "" {
		conf.VPNConfig.InterfaceName = defaultInterfaceName
	}

	if conf.KnownPeers == nil {
		conf.KnownPeers = make(map[string]KnownPeer)
	}
	for peerID := range conf.KnownPeers {
		peer := conf.KnownPeers[peerID]
		if peer.IPAddr == "" {
			peer.IPAddr = conf.GenerateNextIpAddr()
		}
		if peer.DomainName == "" {
			peer.DomainName = awldns.TrimDomainName(peer.DisplayName())
		}
		conf.KnownPeers[peerID] = peer
	}

	if conf.dataDir == "" {
		conf.dataDir = CalcAppDataDir()
	}

	// Create dirs
	// TODO: currently PeerstoreDataDir is not used
	//peerstoreDir := filepath.Join(conf.dataDir, DhtPeerstoreDataDirectory)
	//err := os.MkdirAll(peerstoreDir, dirsPerm)
	//if err != nil {
	//	logger.Warnf("could not create peerstore directory: %v", err)
	//}

	emitter, err := bus.Emitter(new(awlevent.KnownPeerChanged), eventbus.Stateful)
	if err != nil {
		panic(err)
	}
	conf.emitter = emitter
}
