package awl

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/anywherelan/ts-dns/control/controlknobs"
	"github.com/anywherelan/ts-dns/net/dns"
	"github.com/anywherelan/ts-dns/util/dnsname"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/metrics"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/ringbuffer"
	"github.com/anywherelan/awl/service"
	"github.com/anywherelan/awl/vpn"
	"github.com/anywherelan/awl/vpn/sockmark"
)

const (
	logBufSize = 1 << 20
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

//	@title			Anywherelan API
//	@version		0.1
//	@description	Anywherelan API

//	@Host		localhost:8639
//	@BasePath	/api/v0/

//go:generate go run github.com/swaggo/swag/cmd/swag@latest init --parseDependency -g application.go
//go:generate rm -f docs/docs.go docs/swagger.json

type Application struct {
	LogBuffer *ringbuffer.RingBuffer
	logger    *log.ZapEventLogger
	Conf      *config.Config
	Eventbus  awlevent.Bus

	// For tests only:
	ExtraLibp2pOpts          []libp2p.Option
	AllowEmptyBootstrapPeers bool
	// DisableGatewayOSSetup skips the netlink/iptables/route work in
	// service.Gateway, leaving only the in-process bookkeeping. Tests run
	// without root and against a mock TUN that has no kernel netlink
	// presence, so the real setup paths cannot run. Set before Init.
	DisableGatewayOSSetup bool

	ctx        context.Context
	ctxCancel  context.CancelFunc
	vpnDevice  *vpn.Device
	P2p        *p2p.P2p
	Api        *api.Handler
	AuthStatus *service.AuthStatus
	Tunnel     *service.Tunnel
	SOCKS5     *service.SOCKS5
	VPNGateway *service.VPNGateway
	Dns        *DNSService

	// SockMarker abstracts the per-platform socket-marking strategy used to
	// keep libp2p traffic out of the VPN tunnel when gateway mode is on.
	// Callers (notably cmd/gomobile-lib on Android) may set this before
	// Init to inject a platform-specific marker — e.g.
	//   app.SockMarker = sockmark.NewAndroid(protectorFn)
	// Init falls back to sockmark.New() if SockMarker is left nil.
	SockMarker sockmark.Marker
}

func New() *Application {
	return &Application{}
}

func (a *Application) Init(ctx context.Context, tunDevice tun.Device) error {
	a.logger.Info("Application initialization started")

	a.ctx, a.ctxCancel = context.WithCancel(ctx)
	if a.SockMarker == nil {
		a.SockMarker = sockmark.New()
	}
	a.P2p = p2p.NewP2p(a.ctx)
	p2pHost, err := a.P2p.InitHost(a.makeP2pHostConfig())
	if err != nil {
		return err
	}

	privKey := p2pHost.Peerstore().PrivKey(p2pHost.ID())
	a.Conf.SetIdentity(privKey, p2pHost.ID())
	a.logger.Infof("P2P host initialized. My peer_id: %s", p2pHost.ID().String())
	a.logger.Infof("P2P listening on addresses: %v", p2pHost.Addrs())

	if a.Conf.VPNConfig.DisableVPNInterface {
		a.logger.Info("VPN interface is disabled from config")
	} else {
		localIP, netMask := a.Conf.VPNLocalIPMask()
		interfaceName := a.Conf.VPNConfig.InterfaceName
		a.vpnDevice, err = vpn.NewDevice(tunDevice, interfaceName, localIP, netMask)
		if err != nil {
			return fmt.Errorf("failed to init vpn: %v", err)
		}
		a.logger.Infof("VPN interface created. Name: %s CIDR: %s", interfaceName, &net.IPNet{IP: localIP, Mask: netMask})

		a.Tunnel = service.NewTunnel(a.P2p, a.vpnDevice, a.Conf)
		go a.vpnDevice.ReadTUNPackets(a.Tunnel.HandleReadPackets)
	}

	a.P2p.Bootstrap()

	a.Dns = NewDNSService(a.Conf, a.Eventbus, a.ctx, a.logger)
	a.AuthStatus = service.NewAuthStatus(a.P2p, a.Conf, a.Eventbus)
	a.SOCKS5, err = service.NewSOCKS5(a.P2p, a.Conf, a.SockMarker)
	if err != nil {
		return fmt.Errorf("failed to init socks5: %v", err)
	}

	p2pHost.SetStreamHandler(protocol.GetStatusMethod, a.AuthStatus.StatusStreamHandler)
	p2pHost.SetStreamHandler(protocol.AuthMethod, a.AuthStatus.AuthStreamHandler)
	if a.Tunnel != nil {
		p2pHost.SetStreamHandler(protocol.TunnelPacketMethod, a.Tunnel.StreamHandler)
	}
	p2pHost.SetStreamHandler(protocol.Socks5PacketMethod, a.SOCKS5.ProxyStreamHandler)
	p2pHost.SetStreamHandler(protocol.Socks5NoAuthMethod, a.SOCKS5.ProxyStreamHandler)

	if a.Tunnel != nil {
		awlevent.WrapSubscriptionToCallback(a.ctx, func(_ interface{}) {
			a.Tunnel.RefreshPeersList()
		}, a.Eventbus, new(awlevent.KnownPeerChanged))
	}

	a.VPNGateway = service.NewVPNGateway(a.Conf, a.Tunnel, a.vpnDevice, a.P2p, a.SockMarker, a.Dns, a.DisableGatewayOSSetup)

	handler := api.NewHandler(a.Conf, a.P2p, a.AuthStatus, a.Tunnel, a.SOCKS5, a.LogBuffer, a.Dns, a.VPNGateway)
	a.Api = handler
	err = handler.SetupAPI()
	if err != nil {
		return fmt.Errorf("failed to setup api: %v", err)
	}

	go a.P2p.MaintainBackgroundConnections(a.ctx, a.Conf.P2pNode.ReconnectionIntervalSec*time.Second, a.Conf.KnownPeersIds)
	go a.AuthStatus.BackgroundRetryAuthRequests(a.ctx)
	go a.AuthStatus.BackgroundExchangeStatusInfo(a.ctx)
	go a.SOCKS5.ServeConns(a.ctx)

	if !a.Conf.DNS.DisableDNS && !a.Conf.VPNConfig.DisableVPNInterface {
		interfaceName, err := a.vpnDevice.InterfaceName()
		if err != nil {
			a.logger.Errorf("failed to get TUN interface name: %v", err)
		} else {
			a.Dns.initDNS(interfaceName)
		}
	}

	// Metrics
	metrics.SetNodeInfo(config.Version, p2pHost.ID().String())
	cma := &configMetricsAdapter{conf: a.Conf, authStatus: a.AuthStatus, p2p: a.P2p}
	go metrics.StartBackgroundUpdater(a.ctx, cma, a.P2p)

	// VPN Gateway mode setup
	err = a.VPNGateway.SetupAtStartup()
	if err != nil {
		return fmt.Errorf("setup gateway: %v", err)
	}

	a.logger.Info("Application initialized successfully")

	return nil
}

func (a *Application) SetupLoggerAndConfig(appType config.AppType) *log.ZapEventLogger {
	a.Eventbus = eventbus.NewBus()
	// Config
	conf, loadConfigErr := config.LoadConfig(appType, a.Eventbus)
	if loadConfigErr != nil {
		conf = config.NewConfig(appType, a.Eventbus)
	}

	// Logger
	a.LogBuffer = ringbuffer.New(logBufSize)
	syncer := zapcore.NewMultiWriteSyncer(
		zapcore.Lock(zapcore.AddSync(os.Stdout)),
		zapcore.AddSync(a.LogBuffer),
	)

	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.Format("2006-01-02 15:04:05.99"))
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
	a.logger.Infof("Initializing app in %s directory", conf.DataDir())

	return a.logger
}

func (a *Application) Ctx() context.Context {
	return a.ctx
}

func (a *Application) Close() {
	a.Conf.Save()
	// Teardown VPN gateway routes first (restore direct internet before shutting down P2P)
	if a.VPNGateway != nil {
		a.VPNGateway.TeardownAtShutdown()
	}
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

	if a.Tunnel != nil {
		a.Tunnel.Close()
	}
	if a.SOCKS5 != nil {
		a.SOCKS5.Close()
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
		panic(err)
	}

	resourceLimitsConfig := rcmgr.InfiniteLimits
	mgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(resourceLimitsConfig))
	if err != nil {
		panic(err)
	}

	return p2p.HostConfig{
		PrivKeyBytes:             a.Conf.PrivKey(),
		ListenAddrs:              a.Conf.GetListenAddresses(),
		UserAgent:                config.UserAgent,
		BootstrapPeers:           a.Conf.GetBootstrapPeers(),
		AllowEmptyBootstrapPeers: a.AllowEmptyBootstrapPeers,
		EnableAutoRelay:          true,
		// SocketControlFunc is always used: marking happens at dial
		// time on every socket, so libp2p connections opened *before* gateway
		// mode is toggled on at runtime are already exempt from the VPN route.
		SocketControlFunc: a.SockMarker.ControlFunc(),
		Libp2pOpts: append([]libp2p.Option{
			libp2p.EnableRelay(),
			libp2p.EnableAutoNATv2(),
			libp2p.ResourceManager(mgr),
			libp2p.EnableHolePunching(),
			libp2p.NATPortMap(),
			libp2p.PrometheusRegisterer(prometheus.DefaultRegisterer),
		}, a.ExtraLibp2pOpts...),
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

	mu                  sync.Mutex
	dnsHost             string
	dnsFQDN             dnsname.FQDN
	dnsOsConfigurator   dns.OSConfigurator
	dnsResolver         *awldns.Resolver
	upstreamDNS         string
	isAwlDNSSetAsSystem bool
	// forceUpstream forces the awl resolver to capture all queries
	// (MatchDomains=nil) and forward them to the configured public upstream so
	// DNS traverses the tunnel instead of leaking to the system resolver. Set
	// in VPN gateway client mode.
	forceUpstream bool
}

func NewDNSService(conf *config.Config, eventbus awlevent.Bus, ctx context.Context, logger *log.ZapEventLogger) *DNSService {
	return &DNSService{conf: conf, eventbus: eventbus, ctx: ctx, logger: logger}
}

func (a *DNSService) initDNS(interfaceName string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	dnsAddr := a.conf.DNS.ListenAddress
	dnsHost, _, err := net.SplitHostPort(dnsAddr)
	if err != nil {
		a.logger.Errorf("invalid dns listen address %s: %v", dnsAddr, err)
		return
	}
	a.dnsHost = dnsHost

	fqdn, err := dnsname.ToFQDN(awldns.LocalDomain)
	if err != nil {
		panic(err)
	}
	a.dnsFQDN = fqdn

	// TODO(android awldns): on Android this NewResolver cannot bind :53 (needs
	// root) and dnsOsConfigurator.SetDNS below fails (no writable resolv.conf),
	// so awldns is effectively inert there and .awl names do not resolve. The
	// Android host instead points VpnService at DNS.UpstreamDNSAddress directly
	// (see awl-flutter MainActivity.establishTun), which prevents leaks but
	// gives no .awl resolution. A full fix would intercept :53 to a magic awl
	// IP inside the tunnel read-path (userspace netstack),
	// rather than binding an OS socket.
	a.dnsResolver = awldns.NewResolver(dnsAddr)
	a.upstreamDNS = a.conf.DNS.UpstreamDNSAddress
	a.forceUpstream = a.conf.VPNGateway.ClientEnabled
	a.refreshDNSConfigLocked()

	awlevent.WrapSubscriptionToCallback(a.ctx, func(_ interface{}) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.refreshDNSConfigLocked()
	}, a.eventbus, new(awlevent.KnownPeerChanged))

	tsLogger := log.Logger("ts/dnsconf")
	a.dnsOsConfigurator, err = dns.NewOSConfigurator(func(format string, args ...interface{}) {
		tsLogger.Infof(format, args...)
	}, nil, &controlknobs.Knobs{}, interfaceName)
	if err != nil {
		a.logger.Errorf("unable to create dns os configurator: %v", err)
		return
	}

	a.applyOSDNSConfigLocked()
}

// applyOSDNSConfigLocked (re)computes the OS DNS takeover config from the
// current state (split-DNS support, base config, forceUpstream) and pushes it
// to the OS, then refreshes the awl resolver. Caller must hold a.mu and have a
// non-nil dnsOsConfigurator. Safe to call repeatedly.
func (a *DNSService) applyOSDNSConfigLocked() {
	supportsSplitDNS := a.dnsOsConfigurator.SupportsSplitDNS()

	var baseNameservers []netip.Addr
	if !supportsSplitDNS {
		baseOSConfig, err := a.dnsOsConfigurator.GetBaseConfig()
		if err != nil {
			a.logger.Errorf("get base config from os configurator, abort setting os dns: %v", err)
			return
		}
		a.logger.Infof("os does not support split dns. base config: %v", baseOSConfig)
		baseNameservers = baseOSConfig.Nameservers
	}

	matchDomains, upstream := chooseDNSPolicy(a.forceUpstream, supportsSplitDNS, baseNameservers, a.conf.DNS.UpstreamDNSAddress)
	a.upstreamDNS = upstream
	a.refreshDNSConfigLocked()

	// TODO: consider setting SearchDomains = ["awl."] so peers resolve by bare
	// short name (e.g. "mypeer" -> "mypeer.awl") instead of requiring the full
	// .awl suffix. SearchDomains expands single-label queries into FQDNs and is
	// additive to the OS's existing search list (distinct from MatchDomains,
	// which only routes which zones reach the awl resolver). Would likely want a
	// config toggle to gate it.
	//
	// TODO: consider pushing admin.awl into Hosts (a static FQDN->IP map applied
	// to /etc/hosts) instead of (or in addition to) injecting
	// AdminHttpServerDomainName into the resolver name mapping in
	// refreshDNSConfigLocked — that would make the admin UI name resolvable even
	// when the awl :53 resolver itself is not reachable.
	newOSConfig := dns.OSConfig{
		Nameservers:  []netip.Addr{netip.MustParseAddr(a.dnsHost)},
		MatchDomains: matchDomains,
	}
	if err := a.dnsOsConfigurator.SetDNS(newOSConfig); err != nil {
		a.logger.Errorf("set dns config to os configurator: %v", err)
		return
	}
	a.logger.Infof("successfully set dns config to os (forceUpstream=%v, upstream=%s, matchDomains=%v)",
		a.forceUpstream, a.upstreamDNS, matchDomains)
	a.isAwlDNSSetAsSystem = true
}

// chooseDNSPolicy decides which domains the awl resolver should capture and
// which upstream it forwards non-.awl queries to.
//
//   - forceUpstream (VPN gateway client mode): capture everything
//     (MatchDomains=nil) and forward to the configured public upstream so DNS
//     goes through the tunnel — no leak.
//   - split-DNS supported: capture only .awl; other queries are handled by the
//     OS resolver directly, so the awl upstream is unused (kept as the
//     configured default for completeness).
//   - split-DNS unsupported: capture everything and forward to the system's
//     first base nameserver, falling back to the configured default when the OS
//     reports none.
func chooseDNSPolicy(forceUpstream, supportsSplitDNS bool, base []netip.Addr, upstreamCfg string) (matchDomains []dnsname.FQDN, upstream string) {
	awlFQDN, err := dnsname.ToFQDN(awldns.LocalDomain)
	if err != nil {
		panic(err)
	}

	if forceUpstream {
		return nil, upstreamCfg
	}
	if supportsSplitDNS {
		return []dnsname.FQDN{awlFQDN}, upstreamCfg
	}
	// no split DNS: capture everything, forward to the system's base resolver
	if len(base) == 0 {
		return nil, upstreamCfg
	}
	// TODO: use all nameservers in awldns resolver proxy
	return nil, net.JoinHostPort(base[0].String(), awldns.DefaultDNSPort)
}

// ForceUpstreamDNS toggles full-capture mode where the awl resolver intercepts
// all DNS (not just .awl) and forwards it to the configured public upstream, so
// queries traverse the tunnel and do not leak. Driven by the VPN gateway client
// apply/teardown. No-op (returns nil) when DNS was never set up as the system
// resolver (DNS disabled, or Android where the OS DNS takeover does not apply).
// Idempotent.
func (a *DNSService) ForceUpstreamDNS(enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.dnsOsConfigurator == nil || !a.isAwlDNSSetAsSystem {
		return nil
	}
	if a.forceUpstream == enabled {
		return nil
	}
	a.forceUpstream = enabled
	a.applyOSDNSConfigLocked()
	return nil
}

func (a *DNSService) refreshDNSConfigLocked() {
	if a.dnsResolver == nil {
		a.logger.DPanicf("called refreshDNSConfig with nil resolver %v", a.dnsResolver)
		return
	}
	dnsNamesMapping := a.conf.DNSNamesMapping()
	dnsNamesMapping[config.AdminHttpServerDomainName] = config.AdminHttpServerIP
	a.dnsResolver.ReceiveConfiguration(a.upstreamDNS, dnsNamesMapping)
}

func (a *DNSService) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dnsResolver != nil {
		return a.dnsResolver.DNSAddress()
	}
	return ""
}

func (a *DNSService) IsAwlDNSSetAsSystem() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.isAwlDNSSetAsSystem
}

// configMetricsAdapter implements metrics.ConfigMetrics by combining Config and AuthStatus.
type configMetricsAdapter struct {
	conf       *config.Config
	authStatus *service.AuthStatus
	p2p        *p2p.P2p
}

func (a *configMetricsAdapter) GetKnownPeersSnapshot() (total, confirmed, connected int, peerIDs []peer.ID) {
	a.conf.RLock()
	defer a.conf.RUnlock()
	total = len(a.conf.KnownPeers)
	peerIDs = make([]peer.ID, 0, total)
	for _, kp := range a.conf.KnownPeers {
		pid := kp.PeerId()
		peerIDs = append(peerIDs, pid)
		if kp.Confirmed {
			confirmed++
		}
		if a.p2p.IsConnected(pid) {
			connected++
		}
	}
	return
}

func (a *configMetricsAdapter) GetBlockedPeersCount() int {
	a.conf.RLock()
	defer a.conf.RUnlock()
	return len(a.conf.BlockedPeers)
}

func (a *configMetricsAdapter) GetAuthRequestCounts() (ingoing, outgoing int) {
	return a.authStatus.GetAuthRequestCounts()
}
