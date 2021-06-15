package awl

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"time"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/ringbuffer"
	"github.com/anywherelan/awl/service"
	"github.com/anywherelan/awl/vpn"
	"github.com/anywherelan/ts-dns/net/dns"
	"github.com/anywherelan/ts-dns/util/dnsname"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/host"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.zx2c4.com/wireguard/tun"
	"inet.af/netaddr"
)

const (
	logBufSize = 100 * 1024
)

//go:embed static
var frontendStatic embed.FS

func FrontendStatic() fs.FS {
	fsys, err := fs.Sub(frontendStatic, "static")
	if err != nil {
		panic(err)
	}
	return fsys
}

// useAwldns is used for tests
var useAwldns = true

// @title Anywherelan API
// @version 0.1
// @description Anywherelan API

// @Host localhost:8639
// @BasePath /api/v0/

// TODO: move to main package (can't parse here)
//go:generate swag init --parseDependency
//go:generate rm -f docs/docs.go docs/swagger.json

type Application struct {
	LogBuffer *ringbuffer.RingBuffer
	logger    *log.ZapEventLogger
	Conf      *config.Config

	p2pServer  *p2p.P2p
	host       host.Host
	vpnDevice  *vpn.Device
	Api        *api.Handler
	P2pService *service.P2pService
	AuthStatus *service.AuthStatus
	Tunnel     *service.Tunnel

	dnsOsConfigurator dns.OSConfigurator
	dnsResolver       *awldns.Resolver
	upstreamDNS       string
}

func New() *Application {
	return &Application{}
}

func (a *Application) Init(ctx context.Context, tunDevice tun.Device) error {
	p2pSrv := p2p.NewP2p(ctx, a.Conf)
	host, err := p2pSrv.InitHost()
	if err != nil {
		return err
	}
	a.p2pServer = p2pSrv
	a.host = host

	privKey := host.Peerstore().PrivKey(host.ID())
	a.Conf.SetIdentity(privKey, host.ID())
	a.logger.Infof("Host created. We are: %s", host.ID().String())
	a.logger.Infof("Listen interfaces: %v", host.Addrs())

	localIP, netMask := a.Conf.VPNLocalIPMask()
	interfaceName := a.Conf.VPNConfig.InterfaceName
	vpnDevice, err := vpn.NewDevice(tunDevice, interfaceName, localIP, netMask)
	if err != nil {
		return fmt.Errorf("failed to init vpn: %v", err)
	}
	a.vpnDevice = vpnDevice
	a.logger.Infof("Created vpn interface %s: %s", interfaceName, &net.IPNet{IP: localIP, Mask: netMask})

	err = p2pSrv.Bootstrap()
	if err != nil {
		return err
	}

	a.P2pService = service.NewP2p(p2pSrv, a.Conf)
	a.AuthStatus = service.NewAuthStatus(a.P2pService, a.Conf)
	a.Tunnel = service.NewTunnel(a.P2pService, vpnDevice, a.Conf)

	host.SetStreamHandler(protocol.GetStatusMethod, a.AuthStatus.StatusStreamHandler)
	host.SetStreamHandler(protocol.AuthMethod, a.AuthStatus.AuthStreamHandler)
	host.SetStreamHandler(protocol.TunnelPacketMethod, a.Tunnel.StreamHandler)

	handler := api.NewHandler(a.Conf, a.P2pService, a.AuthStatus, a.Tunnel, a.LogBuffer)
	a.Api = handler
	err = handler.SetupAPI()
	if err != nil {
		return fmt.Errorf("failed to setup api: %v", err)
	}

	go a.P2pService.MaintainBackgroundConnections(a.Conf.P2pNode.ReconnectionIntervalSec)
	go a.AuthStatus.BackgroundRetryAuthRequests()
	go a.AuthStatus.BackgroundExchangeStatusInfo()

	if useAwldns {
		a.initDNS()
	}

	return nil
}

func (a *Application) SetupLoggerAndConfig() *log.ZapEventLogger {
	// Config
	conf, loadConfigErr := config.LoadConfig()
	if loadConfigErr != nil {
		conf = config.NewConfig()
	}

	// Logger
	a.LogBuffer = ringbuffer.New(logBufSize)
	syncer := zapcore.NewMultiWriteSyncer(
		zapcore.Lock(zapcore.AddSync(os.Stdout)),
		zapcore.AddSync(a.LogBuffer),
	)

	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.Format("2006-01-02 15:04:05"))
	}
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
	zapCore := zapcore.NewCore(consoleEncoder, syncer, zapcore.InfoLevel)

	lvl := conf.LogLevel()
	opts := []zap.Option{zap.AddStacktrace(zapcore.ErrorLevel)}
	if conf.DevMode() {
		opts = append(opts, zap.Development())
	}

	log.SetupLogging(zapCore, func(name string) zapcore.Level {
		switch {
		case strings.HasPrefix(name, "awl"):
			return lvl
		case name == "swarm2":
			// TODO: решить какой выставлять
			//return zapcore.InfoLevel // REMOVE
			return zapcore.ErrorLevel
		case name == "relay":
			return zapcore.WarnLevel
		case name == "connmgr":
			return zapcore.WarnLevel
		case name == "autonat":
			return zapcore.WarnLevel
		default:
			return zapcore.InfoLevel
		}
	},
		opts...,
	)

	a.logger = log.Logger("awl")
	a.Conf = conf

	if loadConfigErr != nil {
		a.logger.Warnf("failed to read config file, creating new one: %v", loadConfigErr)
	}
	a.logger.Infof("initialize app in %s directory", conf.DataDir())

	return a.logger
}

func (a *Application) Close() {
	if a.Api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := a.Api.Shutdown(ctx)
		if err != nil {
			a.logger.Errorf("closing api server: %v", err)
		}
	}
	if a.p2pServer != nil {
		err := a.p2pServer.Close()
		if err != nil {
			a.logger.Errorf("closing p2p server: %v", err)
		}
	}
	if a.vpnDevice != nil {
		err := a.vpnDevice.Close()
		if err != nil {
			a.logger.Errorf("closing vpn: %v", err)
		}
	}
	if a.dnsOsConfigurator != nil {
		err := a.dnsOsConfigurator.Close()
		if err != nil {
			a.logger.Errorf("closing dns configurator: %v", err)
		}
	}
	if a.dnsResolver != nil {
		a.dnsResolver.Close()
	}
	a.Conf.Save()
}

func (a *Application) initDNS() {
	interfaceName := a.Conf.VPNConfig.InterfaceName
	var err error
	a.dnsResolver = awldns.NewResolver()
	a.Conf.RegisterOnKnownPeersChanged(a.refreshDNSConfig)
	defer a.refreshDNSConfig()

	tsLogger := log.Logger("ts/dnsconf")
	a.dnsOsConfigurator, err = dns.NewOSConfigurator(func(format string, args ...interface{}) {
		tsLogger.Infof(format, args...)
	}, interfaceName)
	if err != nil {
		a.logger.Errorf("create dns os configurator: %v", err)
		return
	}

	fqdn, err := dnsname.ToFQDN(awldns.LocalDomain)
	if err != nil {
		panic(err)
	}
	newOSConfig := dns.OSConfig{
		Nameservers:  []netaddr.IP{netaddr.MustParseIP(awldns.DNSIp)},
		MatchDomains: []dnsname.FQDN{fqdn},
	}

	if !a.dnsOsConfigurator.SupportsSplitDNS() {
		newOSConfig.MatchDomains = nil
		baseOSConfig, err := a.dnsOsConfigurator.GetBaseConfig()
		if err != nil {
			a.logger.Errorf("get base config from os configurator, abort setting os dns: %v", err)
			return
		}

		a.logger.Infof("os does not support split dns. base config: %v", baseOSConfig)
		if len(baseOSConfig.Nameservers) == 0 {
			a.logger.Errorf("got zero nameservers from os configurator")
		} else {
			// TODO: use all nameservers in awldns resolver proxy
			a.upstreamDNS = net.JoinHostPort(baseOSConfig.Nameservers[0].String(), awldns.DefaultDNSPort)
		}
	}

	err = a.dnsOsConfigurator.SetDNS(newOSConfig)
	if err != nil {
		a.logger.Errorf("set dns config to os configurator: %v", err)
	} else {
		a.logger.Info("successfully set dns config to os")
	}
}

func (a *Application) refreshDNSConfig() {
	if a.Api == nil || a.dnsResolver == nil {
		a.logger.DPanicf("called refreshDNSConfig with nil api %v or resolver %v", a.Api, a.dnsResolver)
		return
	}
	dnsNamesMapping := a.Conf.DNSNamesMapping()
	apiHost, _, _ := net.SplitHostPort(a.Api.Address())
	dnsNamesMapping[config.HttpServerDomainName] = apiHost
	a.dnsResolver.ReceiveConfiguration(a.upstreamDNS, dnsNamesMapping)
}
