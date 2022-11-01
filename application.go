package awl

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/ringbuffer"
	"github.com/anywherelan/awl/service"
	"github.com/anywherelan/awl/vpn"
	"github.com/anywherelan/ts-dns/net/dns"
	"github.com/anywherelan/ts-dns/util/dnsname"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
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

//go:generate swag init --parseDependency -g application.go
//go:generate rm -f docs/docs.go docs/swagger.json

type Application struct {
	LogBuffer *ringbuffer.RingBuffer
	logger    *log.ZapEventLogger
	Conf      *config.Config
	Eventbus  awlevent.Bus

	ctx        context.Context
	ctxCancel  context.CancelFunc
	vpnDevice  *vpn.Device
	P2p        *p2p.P2p
	Api        *api.Handler
	AuthStatus *service.AuthStatus
	Tunnel     *service.Tunnel
	Dns        *DNSService
}

func New() *Application {
	return &Application{}
}

func (a *Application) Init(ctx context.Context, tunDevice tun.Device) error {
	a.ctx, a.ctxCancel = context.WithCancel(ctx)
	a.P2p = p2p.NewP2p(a.ctx)
	p2pHost, err := a.P2p.InitHost(a.makeP2pHostConfig())
	if err != nil {
		return err
	}

	privKey := p2pHost.Peerstore().PrivKey(p2pHost.ID())
	a.Conf.SetIdentity(privKey, p2pHost.ID())
	a.logger.Infof("Host created. We are: %s", p2pHost.ID().String())
	a.logger.Infof("Listen interfaces: %v", p2pHost.Addrs())

	localIP, netMask := a.Conf.VPNLocalIPMask()
	interfaceName := a.Conf.VPNConfig.InterfaceName
	vpnDevice, err := vpn.NewDevice(tunDevice, interfaceName, localIP, netMask)
	if err != nil {
		return fmt.Errorf("failed to init vpn: %v", err)
	}
	a.vpnDevice = vpnDevice
	a.logger.Infof("Created vpn interface %s: %s", interfaceName, &net.IPNet{IP: localIP, Mask: netMask})

	err = a.P2p.Bootstrap()
	if err != nil {
		return err
	}

	a.Dns = NewDNSService(a.Conf, a.Eventbus, a.ctx, a.logger)
	a.AuthStatus = service.NewAuthStatus(a.P2p, a.Conf, a.Eventbus)
	a.Tunnel = service.NewTunnel(a.P2p, vpnDevice, a.Conf)

	p2pHost.SetStreamHandler(protocol.GetStatusMethod, a.AuthStatus.StatusStreamHandler)
	p2pHost.SetStreamHandler(protocol.AuthMethod, a.AuthStatus.AuthStreamHandler)
	p2pHost.SetStreamHandler(protocol.TunnelPacketMethod, a.Tunnel.StreamHandler)

	awlevent.WrapSubscriptionToCallback(a.ctx, func(_ interface{}) {
		a.Tunnel.RefreshPeersList()
	}, a.Eventbus, new(awlevent.KnownPeerChanged))

	handler := api.NewHandler(a.Conf, a.P2p, a.AuthStatus, a.Tunnel, a.LogBuffer, a.Dns)
	a.Api = handler
	err = handler.SetupAPI()
	if err != nil {
		return fmt.Errorf("failed to setup api: %v", err)
	}
	// because of cyclic dependency between api and dns
	a.Dns.api = a.Api

	go a.P2p.MaintainBackgroundConnections(a.ctx, a.Conf.P2pNode.ReconnectionIntervalSec*time.Second, a.Conf.KnownPeersIds)
	go a.AuthStatus.BackgroundRetryAuthRequests(a.ctx)
	go a.AuthStatus.BackgroundExchangeStatusInfo(a.ctx)

	if useAwldns {
		interfaceName, err := a.vpnDevice.InterfaceName()
		if err != nil {
			a.logger.Errorf("failed to get TUN interface name: %v", err)
			return nil
		}
		a.Dns.initDNS(interfaceName)
	}

	return nil
}

func (a *Application) SetupLoggerAndConfig() *log.ZapEventLogger {
	a.Eventbus = eventbus.NewBus()
	// Config
	conf, loadConfigErr := config.LoadConfig(a.Eventbus)
	if loadConfigErr != nil {
		conf = config.NewConfig(a.Eventbus)
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
		if strings.HasPrefix(name, "awl") {
			return lvl
		}
		switch name {
		case "swarm2", "relay", "connmgr", "autonat":
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
	a.logger.Infof("Anywherelan %s (%s %s-%s)", config.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	a.logger.Infof("initialize app in %s directory", conf.DataDir())

	return a.logger
}

func (a *Application) Ctx() context.Context {
	return a.ctx
}

func (a *Application) Close() {
	if a.ctxCancel != nil {
		a.ctxCancel()
	}
	if a.Api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := a.Api.Shutdown(ctx)
		if err != nil {
			a.logger.Errorf("closing api server: %v", err)
		}
	}
	if a.P2p != nil {
		err := a.P2p.Close()
		if err != nil {
			a.logger.Errorf("closing p2p server: %v", err)
		}
	}
	if a.Dns != nil {
		a.Dns.Close()
	}
	if a.Tunnel != nil {
		a.Tunnel.Close()
	}
	if a.vpnDevice != nil {
		err := a.vpnDevice.Close()
		if err != nil {
			a.logger.Errorf("closing vpn: %v", err)
		}
	}
	a.Conf.Save()
}

func (a *Application) makeP2pHostConfig() p2p.HostConfig {
	// TODO: use persistent datastore. Check out badger2. Old badger datastore constantly use disk io
	peerstore, err := pstoremem.NewPeerstore()
	if err != nil {
		panic(peerstore)
	}

	return p2p.HostConfig{
		PrivKeyBytes:   a.Conf.PrivKey(),
		ListenAddrs:    a.Conf.GetListenAddresses(),
		UserAgent:      config.UserAgent,
		BootstrapPeers: a.Conf.GetBootstrapPeers(),
		Libp2pOpts: []libp2p.Option{
			libp2p.EnableRelay(),
			libp2p.EnableAutoRelay(
				autorelay.WithNumRelays(p2p.DesiredRelays),
				autorelay.WithBootDelay(p2p.RelayBootDelay),
				// TODO: remove after next minor release
				autorelay.WithCircuitV1Support(),
				autorelay.WithStaticRelays(a.Conf.GetBootstrapPeers()),
			),
			libp2p.EnableHolePunching(),
			libp2p.NATPortMap(),
		},
		ConnManager: struct {
			LowWater    int
			HighWater   int
			GracePeriod time.Duration
		}{
			LowWater:    50,
			HighWater:   100,
			GracePeriod: time.Minute,
		},
		Peerstore:    peerstore,
		DHTDatastore: dssync.MutexWrap(ds.NewMapDatastore()),
	}
}

type DNSService struct {
	conf     *config.Config
	eventbus awlevent.Bus
	ctx      context.Context
	logger   *log.ZapEventLogger
	api      *api.Handler

	dnsOsConfigurator   dns.OSConfigurator
	dnsResolver         *awldns.Resolver
	upstreamDNS         string
	isAwlDNSSetAsSystem bool
}

func NewDNSService(conf *config.Config, eventbus awlevent.Bus, ctx context.Context, logger *log.ZapEventLogger) *DNSService {
	return &DNSService{conf: conf, eventbus: eventbus, ctx: ctx, logger: logger}
}

func (a *DNSService) initDNS(interfaceName string) {
	var err error
	a.dnsResolver = awldns.NewResolver()
	awlevent.WrapSubscriptionToCallback(a.ctx, func(_ interface{}) {
		a.refreshDNSConfig()
	}, a.eventbus, new(awlevent.KnownPeerChanged))
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
			a.logger.Errorf("got zero nameservers from os configurator, use %s as default", awldns.DefaultUpstreamDNSAddress)
			a.upstreamDNS = awldns.DefaultUpstreamDNSAddress
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
		a.isAwlDNSSetAsSystem = true
	}
}

func (a *DNSService) refreshDNSConfig() {
	if a.api == nil || a.dnsResolver == nil {
		a.logger.DPanicf("called refreshDNSConfig with nil api %v or resolver %v", a.api, a.dnsResolver)
		return
	}
	dnsNamesMapping := a.conf.DNSNamesMapping()
	apiHost, _, _ := net.SplitHostPort(a.api.Address())
	dnsNamesMapping[config.HttpServerDomainName] = apiHost
	a.dnsResolver.ReceiveConfiguration(a.upstreamDNS, dnsNamesMapping)
}

func (a *DNSService) Close() {
	if a.dnsOsConfigurator != nil {
		err := a.dnsOsConfigurator.Close()
		if err != nil {
			a.logger.Errorf("closing dns configurator: %v", err)
		}
	}
	if a.dnsResolver != nil {
		a.dnsResolver.Close()
	}
}

func (a *DNSService) AwlDNSAddress() string {
	if a.dnsResolver != nil {
		return a.dnsResolver.DNSAddress()
	}
	return ""
}

func (a *DNSService) IsAwlDNSSetAsSystem() bool {
	return a.isAwlDNSSetAsSystem
}
