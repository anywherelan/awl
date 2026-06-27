package service

import (
	"fmt"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/vpn"
	"github.com/anywherelan/awl/vpn/routes"
	"github.com/anywherelan/awl/vpn/sockmark"
)

// DNSReconfigurer is the narrow slice of the DNS service the gateway needs to
// prevent DNS leaks in client mode. Implemented by awl.DNSService; injected to
// avoid a service -> awl import cycle. May be nil.
type DNSReconfigurer interface {
	// ForceUpstreamDNS toggles full-capture mode where all DNS is routed
	// through the tunnel to a public upstream. Idempotent; a no-op when DNS is
	// not active as the system resolver.
	ForceUpstreamDNS(enabled bool) error
}

// VPNGateway owns the runtime state for VPN gateway mode (both client and
// server sides) — the OS-level routes / NAT / iptables, plus the lifecycle
// glue that ties them to the persisted VPNGatewayConfig and the Tunnel's
// runtime peer state.
//
// Responsibility split with Tunnel:
//   - Tunnel owns the packet-path state (gateway peer pointer, fwmark
//     forwarding decisions, connectivity observation via p2p events).
//   - VPNGateway owns OS-level state (routes, NAT) and the lifecycle methods
//     called from the API and from Application.Init / Close.
//
// VPNGateway calls into Tunnel for the runtime bind/unbind operations
// (SetVPNGatewayPeer / ClearVPNGatewayPeer / SetVPNGatewayServerEnabled).
// When the VPN interface is disabled, Tunnel is nil and VPNGateway writes
// directly to the config — the API endpoints still succeed, persistence still
// works, and the next start with a real TUN will pick the values up.
type VPNGateway struct {
	conf       *config.Config
	tunnel     *Tunnel
	device     *vpn.Device
	p2p        P2p
	sockMarker sockmark.Marker
	dns        DNSReconfigurer
	logger     *log.ZapEventLogger

	// disableOSSetup skips the netlink/iptables/route work in
	// applyClient/applyServer, leaving only the in-process bookkeeping.
	// Tests run without root and against a mock TUN that has no kernel
	// netlink presence, so the real setup paths cannot run there.
	disableOSSetup bool

	// mu serialises apply/teardown across the API, startup and shutdown
	// paths. Connectivity to the bound gateway peer is observed via p2p
	// events inside Tunnel — no background goroutine here.
	mu               sync.Mutex
	clientRouteState *routes.RouteState
	serverNATState   *routes.NATState
}

// NewVPNGateway constructs a VPNGateway service. tunnel may be nil when the
// VPN interface is disabled; the API methods still work but only update the
// persisted config.
func NewVPNGateway(conf *config.Config, tunnel *Tunnel, device *vpn.Device, p2p P2p, sockMarker sockmark.Marker, dns DNSReconfigurer, disableOSSetup bool) *VPNGateway {
	return &VPNGateway{
		conf:           conf,
		tunnel:         tunnel,
		device:         device,
		p2p:            p2p,
		sockMarker:     sockMarker,
		dns:            dns,
		disableOSSetup: disableOSSetup,
		logger:         log.Logger("awl/service/vpn_gateway"),
	}
}

// VPNGatewayClientSupported reports whether client-side VPN gateway mode can
// run on this OS/build. Linux has the full implementation; Android can run as
// a client but only via the Android host's VpnService.Builder, so runtime API
// changes only mutate the persisted config and take effect on next startup
// (see EnableClient / DisableClient).
func VPNGatewayClientSupported() error {
	switch runtime.GOOS {
	case "linux", "android":
		return nil
	case "windows":
		return fmt.Errorf("VPN gateway client mode is not yet supported on Windows; " +
			"see vpn/sockmark/sockmark_windows.go for outstanding work")
	case "darwin":
		return fmt.Errorf("VPN gateway client mode is not yet supported on macOS")
	default:
		return fmt.Errorf("VPN gateway client mode is not supported on %s", runtime.GOOS)
	}
}

// VPNGatewayServerSupported reports whether server-side VPN gateway mode can
// run on this OS/build. Currently only Linux: Android exit-node support
// requires root or special system config, macOS lacks NAT/route glue, and the
// Windows path (vpn/routes/nat_windows.go) is not yet safe to enable.
func VPNGatewayServerSupported() error {
	if runtime.GOOS == "linux" {
		return nil
	}
	return fmt.Errorf("VPN gateway server mode is not supported on %s", runtime.GOOS)
}

// VPNGatewaySupported reports whether the full VPN gateway feature set (both
// client and server) can run on this OS/build. Equivalent to
// VPNGatewayServerSupported — server is the strictly stronger requirement.
// Kept as a convenience for callers (awl-tray menu construction) that gate the
// whole feature UI on full support.
func VPNGatewaySupported() error {
	return VPNGatewayServerSupported()
}

// ListAvailableVPNGateways returns peers that are currently a valid VPN
// gateway target — i.e. they allow being used as an exit node and have VPN
// gateway server enabled on their side. Peers that allow exit-node use only
// for SOCKS5 are not included here. Returned slice is sorted by connected
// first, then by display name.
func (g *VPNGateway) ListAvailableVPNGateways() []entity.AvailableVPNGateway {
	g.conf.RLock()
	gateways := []entity.AvailableVPNGateway{}
	for _, kp := range g.conf.KnownPeers {
		if !kp.CanUseAsVPNGateway() {
			continue
		}
		gateways = append(gateways, entity.AvailableVPNGateway{
			PeerID:    kp.PeerID,
			PeerName:  kp.DisplayName(),
			Connected: g.p2p.IsConnected(kp.PeerId()),
		})
	}
	g.conf.RUnlock()

	slices.SortFunc(gateways, func(a, b entity.AvailableVPNGateway) int {
		if a.Connected != b.Connected {
			if a.Connected {
				return -1
			}
			return 1
		}
		return strings.Compare(a.PeerName, b.PeerName)
	})
	return gateways
}

// EnableClient turns on VPN gateway client mode using the given peer as the
// gateway, applying OS-level routes immediately. Atomic: rolls back the
// tunnel binding on apply failure.
//
// On android the OS-level apply (routes.SetupGatewayRoutes / sockmark) is a
// no-op — routing is owned by the host's VpnService.Builder. This call flips
// the in-memory tunnel binding and persists config; the host then re-establishes
// the VpnService with the new routes and hot-swaps the fresh tun fd into the
// running app (see cmd/gomobile-lib UpdateTunDevice and vpn.SwappableTUN), so the
// change takes effect without restarting the daemon.
func (g *VPNGateway) EnableClient(gatewayPeerID peer.ID) error {
	if err := VPNGatewayClientSupported(); err != nil {
		return err
	}
	if g.tunnel == nil {
		return fmt.Errorf("VPN interface is disabled, cannot enable gateway")
	}
	if err := g.tunnel.SetVPNGatewayPeer(gatewayPeerID); err != nil {
		return err
	}
	if err := g.applyClient(); err != nil {
		g.tunnel.ClearVPNGatewayPeer()
		return err
	}
	return nil
}

// DisableClient turns off VPN gateway client mode and persists the choice.
// Tears OS-level state down first so return traffic from the gateway peer can
// still flow while we still hold the tunnel binding; once routes are gone,
// packets fall back to the regular awl path. Idempotent. Handles tunnel==nil
// (VPN interface disabled) by writing the config directly.
func (g *VPNGateway) DisableClient() {
	if g.tunnel != nil {
		g.teardownClient()
		g.tunnel.ClearVPNGatewayPeer()
		return
	}
	g.conf.Lock()
	g.conf.VPNGateway.ClientEnabled = false
	g.conf.VPNGateway.GatewayPeerID = ""
	g.conf.SaveLocked()
	g.conf.Unlock()
}

// SetServerEnabled toggles whether this node serves as a VPN gateway for
// permitted peers. Persisted; the new value propagates to other peers via the
// next status exchange. NAT / iptables state is (re)applied at runtime — no
// restart required. Handles tunnel==nil (VPN interface disabled) by writing
// the config directly.
func (g *VPNGateway) SetServerEnabled(enabled bool) error {
	if enabled {
		if err := VPNGatewayServerSupported(); err != nil {
			return err
		}
		if err := g.applyServer(); err != nil {
			return err
		}
		if g.tunnel != nil {
			g.tunnel.SetVPNGatewayServerEnabled(true)
		} else {
			g.conf.Lock()
			g.conf.VPNGateway.ServerEnabled = true
			g.conf.SaveLocked()
			g.conf.Unlock()
		}
		return nil
	}

	if g.tunnel != nil {
		g.tunnel.SetVPNGatewayServerEnabled(false)
	} else {
		g.conf.Lock()
		g.conf.VPNGateway.ServerEnabled = false
		g.conf.SaveLocked()
		g.conf.Unlock()
	}
	g.teardownServer()
	return nil
}

// SetupAtStartup is the boot-time path
func (g *VPNGateway) SetupAtStartup() error {
	g.conf.RLock()
	gw := g.conf.VPNGateway
	g.conf.RUnlock()

	if gw.ServerEnabled {
		if err := VPNGatewayServerSupported(); err != nil {
			g.logger.Errorf("VPN gateway server not enabled at startup: %v", err)
		} else if err := g.SetServerEnabled(true); err != nil {
			return fmt.Errorf("couldn't enable VPN gateway server at startup: %v", err)
		}
	}

	if gw.ClientEnabled && gw.GatewayPeerID != "" {
		if err := VPNGatewayClientSupported(); err != nil {
			g.logger.Errorf("VPN gateway client not enabled at startup: %v", err)
			return nil
		}
		gatewayPeerID, err := peer.Decode(gw.GatewayPeerID)
		if err != nil {
			g.logger.Errorf("failed to decode gateway peer id, disabling VPN gateway client and continue startup: %v", err)
			// removing invalid setting from config
			g.DisableClient()
			return nil
		}

		err = g.EnableClient(gatewayPeerID)
		if err != nil {
			return fmt.Errorf("couldn't enable VPN gateway client at startup: %v", err)
		}
	}

	return nil
}

// TeardownAtShutdown reverses SetupAtStartup. It only restores OS-level
// state (routes, NAT). It does NOT touch Tunnel's gateway peer pointer or the
// persisted config — the daemon should resume gateway mode on next start with
// the same gateway peer.
func (g *VPNGateway) TeardownAtShutdown() {
	g.teardownClient()
	g.teardownServer()
}

// IsClientActive reports whether client-side gateway routes are currently
// installed. Test-friendly accessor; production code should not depend on
// this — the canonical "is gateway on" signal is config.VPNGateway.ClientEnabled
// or PeerInfo.VPNGateway.
func (g *VPNGateway) IsClientActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clientRouteState != nil
}

// IsServerActive reports whether VPN gateway server NAT is currently installed.
// Test-friendly accessor.
func (g *VPNGateway) IsServerActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.serverNATState != nil
}

// ClientRouteState returns the current route state pointer (or nil). For
// tests that need pointer-identity comparisons (idempotent re-enable must
// not reinstall routes).
func (g *VPNGateway) ClientRouteState() *routes.RouteState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clientRouteState
}

// applyServer brings up VPN gateway server NAT (iptables MASQUERADE +
// ip_forward + AWL-FORWARD chain). Idempotent: if NAT is already configured,
// it is a no-op.
func (g *VPNGateway) applyServer() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.serverNATState != nil {
		return nil
	}
	if err := VPNGatewayServerSupported(); err != nil {
		return err
	}
	if g.device == nil {
		return fmt.Errorf("VPN interface is disabled, cannot serve as gateway")
	}
	if g.disableOSSetup {
		// Keep state tracking working (so teardown is symmetric) without
		// touching the kernel.
		g.serverNATState = &routes.NATState{}
		return nil
	}

	tunName, err := g.device.InterfaceName()
	if err != nil {
		return fmt.Errorf("get TUN name for NAT: %w", err)
	}
	localIP, netMask := g.conf.VPNLocalIPMask()
	awlSubnet := (&net.IPNet{IP: localIP.Mask(netMask), Mask: netMask}).String()

	natState, err := routes.SetupNAT(awlSubnet, tunName)
	if err != nil {
		return fmt.Errorf("setup NAT: %w", err)
	}
	g.serverNATState = natState
	g.logger.Infof("VPN gateway server NAT configured for subnet %s on %s", awlSubnet, tunName)
	return nil
}

// teardownServer reverses applyServer. Idempotent.
func (g *VPNGateway) teardownServer() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.serverNATState == nil {
		return
	}
	if !g.disableOSSetup {
		if err := routes.TeardownNAT(g.serverNATState); err != nil {
			g.logger.Errorf("teardown NAT: %v", err)
		}
	}
	g.serverNATState = nil
}

// applyClient installs the policy-routing rules + TUN default route. The
// Tunnel must already be bound to the gateway peer (Tunnel.SetVPNGatewayPeer)
// before calling this — applyClient reads the gateway peer ID from the config
// purely for logging. Idempotent: if routes are already installed they are
// reused.
func (g *VPNGateway) applyClient() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := VPNGatewayClientSupported(); err != nil {
		return err
	}
	if g.tunnel == nil {
		return fmt.Errorf("VPN interface is disabled, cannot enable gateway")
	}

	g.conf.RLock()
	gatewayPeerIDStr := g.conf.VPNGateway.GatewayPeerID
	g.conf.RUnlock()
	if gatewayPeerIDStr == "" {
		return fmt.Errorf("no VPN gateway peer configured")
	}
	gatewayPeerID, err := peer.Decode(gatewayPeerIDStr)
	if err != nil {
		return fmt.Errorf("invalid VPN gateway peer ID %q: %w", gatewayPeerIDStr, err)
	}

	if g.clientRouteState != nil {
		return nil
	}

	if g.disableOSSetup {
		g.clientRouteState = &routes.RouteState{}
	} else {
		tunName, err := g.device.InterfaceName()
		if err != nil {
			return fmt.Errorf("get TUN name for gateway routes: %w", err)
		}
		routeState, err := routes.SetupGatewayRoutes(tunName, g.sockMarker.FWMark())
		if err != nil {
			return fmt.Errorf("setup gateway routes: %w", err)
		}
		g.clientRouteState = routeState
	}

	// Route all DNS through the tunnel to prevent leaks. Done after routes are
	// up so a route failure never leaves DNS pointed at a public resolver with
	// no tunnel. A DNS failure is a leak (degradation), not loss of
	// connectivity, so we log it but do not fail EnableClient. No-op when DNS
	// is not active (disabled, or Android — handled by the host's VpnService).
	if g.dns != nil {
		if err := g.dns.ForceUpstreamDNS(true); err != nil {
			g.logger.Errorf("force upstream DNS on gateway client enable (continuing, DNS may leak): %v", err)
		}
	}

	g.logger.Infof("VPN gateway client mode enabled, gateway peer: %s", gatewayPeerID)

	return nil
}

// teardownClient removes the gateway routes. Idempotent. Does NOT touch
// Tunnel's gateway peer pointer / config — callers must clear those
// separately (Tunnel.ClearVPNGatewayPeer).
func (g *VPNGateway) teardownClient() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.clientRouteState == nil {
		return
	}
	if !g.disableOSSetup {
		if err := routes.TeardownGatewayRoutes(g.clientRouteState); err != nil {
			g.logger.Errorf("teardown gateway routes: %v", err)
		}
	}
	g.clientRouteState = nil

	// Restore normal DNS (split-DNS / system resolver). No-op when DNS is not
	// active as the system resolver.
	if g.dns != nil {
		if err := g.dns.ForceUpstreamDNS(false); err != nil {
			g.logger.Errorf("restore DNS on gateway client teardown: %v", err)
		}
	}
}
