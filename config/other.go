package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ipfs/go-log/v2"
	"github.com/multiformats/go-multiaddr"
	"github.com/peerlan/peerlan/application/pkg"
)

const (
	filesPerm = 0600
	dirsPerm  = 0700
)

// TODO: move to Config struct?
var logger = log.Logger("peerlan/config")

var DefaultBootstrapPeers []multiaddr.Multiaddr

func init() {
	for _, s := range []string{
		"/ip4/45.67.230.153/tcp/6350/p2p/QmQ4JKHL6tpYGNSCT3bvqANgfBaiPEzkwCubimfrXhtXP4",
		"/ip4/45.67.230.153/udp/6450/quic/p2p/QmQ4JKHL6tpYGNSCT3bvqANgfBaiPEzkwCubimfrXhtXP4",

		"/ip4/212.237.53.149/tcp/6350/p2p/QmQD18j92bSDeBrDHwdMFxQ75MQC82touaY93Z22GDPjuo",
		"/ip4/212.237.53.149/udp/6450/quic/p2p/QmQD18j92bSDeBrDHwdMFxQ75MQC82touaY93Z22GDPjuo",
		"/ip6/2a00:6d40:72:d95::1/tcp/7350/p2p/QmQD18j92bSDeBrDHwdMFxQ75MQC82touaY93Z22GDPjuo",
		"/ip6/2a00:6d40:72:d95::1/udp/7450/quic/p2p/QmQD18j92bSDeBrDHwdMFxQ75MQC82touaY93Z22GDPjuo",
	} {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			logger.DPanicf("parse multiaddr: %v", err)
			continue
		}
		DefaultBootstrapPeers = append(DefaultBootstrapPeers, ma)
	}
}

var (
	suitableDataDir string
)

func CalcAppDataDir() (dataDir string) {
	defer func() {
		logger.Debugf("initialize app in %s directory", dataDir)
	}()

	if envDir := os.Getenv(AppDataDirEnvKey); envDir != "" {
		suitableDataDir = envDir
		err := os.MkdirAll(envDir, dirsPerm)
		if err != nil {
			logger.Warnf("could not create peerlan data directory from env: %v", err)
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
		suitableDataDir = executableDir
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
		logger.Warnf("could not create peerlan data directory in user dir: %v", err)
		return ""
	}
	suitableDataDir = userDataDir

	return userDataDir
}

func NewConfig() *Config {
	conf := &Config{}
	setDefaults(conf)
	return conf
}

// Exists for testing purposes.
func NewConfigInDir(dir string) *Config {
	conf := new(Config)
	conf.dataDir = dir
	setDefaults(conf)
	return conf
}

func LoadConfig() (*Config, error) {
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
	setDefaults(conf)
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

	logger.Info("Imported new config")
	return nil
}

func setDefaults(conf *Config) {
	// P2pNode
	if len(conf.P2pNode.ListenAddresses) == 0 {
		multiaddrs := make([]multiaddr.Multiaddr, 0, 4)
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
	// TODO: remove after public release
	if conf.LoggerLevel != "dev" {
		conf.LoggerLevel = "debug"
	}
	//if conf.LoggerLevel == "" {
	//	conf.LoggerLevel = "info"
	//}
	if conf.HttpListenAddress == "" {
		conf.HttpListenAddress = "127.0.0.1:" + strconv.Itoa(DefaultHTTPPort)
	}
	conf.Version = pkg.Version
	if conf.KnownPeers == nil {
		conf.KnownPeers = make(map[string]KnownPeer)
	}
	for peerID := range conf.KnownPeers {
		peer := conf.KnownPeers[peerID]
		if peer.AllowedRemotePorts == nil {
			peer.AllowedRemotePorts = make(map[int]RemoteConnConfig)
		}
		if peer.AllowedLocalPorts == nil {
			peer.AllowedLocalPorts = make(map[int]LocalConnConfig)
		}
		if peer.IPAddr == "" {
			peer.IPAddr = conf.GenerateNextIpAddr()
		}
		conf.KnownPeers[peerID] = peer
	}
	if conf.dataDir == "" {
		conf.dataDir = suitableDataDir
	}

	// Create dirs
	// TODO: currently PeerstoreDataDir is not used
	//peerstoreDir := filepath.Join(conf.dataDir, DhtPeerstoreDataDirectory)
	//err := os.MkdirAll(peerstoreDir, dirsPerm)
	//if err != nil {
	//	logger.Warnf("could not create peerstore directory: %v", err)
	//}
}
