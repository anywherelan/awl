//go:build windows

package netstate

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const (
	// Debounce parameters for network-change notifications, borrowed from
	// WireGuard for Windows (tunnel/defaultroutemonitor.go): coalesce bursts
	// for 150ms, but never delay a re-detection beyond 2s if the burst keeps
	// going (interface storms during docking/undocking).
	debounceInterval = 150 * time.Millisecond
	debounceBurstMax = 2 * time.Second

	// sweepInterval paces the registry liveness sweep. On a stable network
	// re-apply (the other cleanup point) may not run for weeks; the sweep
	// guarantees closed sockets don't stay pinned by their RawConn either
	// way. The registry holds a handful of entries, so this is nearly free.
	sweepInterval = 2 * time.Minute
)

// Manager owns the Windows OS network state behind AWL's VPN gateway feature:
// the always-on socket marking (IP_UNICAST_IF binding to the physical uplink
// NIC — mechanism and lifecycle in sockmark_windows.go) and the runtime state
// of gateway mode — the client routes and the server NAT.
//
// Enable/Disable methods are idempotent and safe for concurrent use. The
// internal mutex only guards the Manager's own state; the orchestration
// above (service.VPNGateway) still serialises whole enable/disable
// transactions — config, tunnel binding, DNS and the calls here — under its
// own lock.
type Manager struct {
	// index4/index6 are the current uplink interface indexes per address
	// family (0 = unknown right now), tracked separately because on
	// multihomed hosts the IPv6 default route may live on a different NIC
	// than the IPv4 one. Atomics, not m.mu: ControlFunc reads them on every
	// dial — a hot path that must not contend with enable/disable
	// transactions.
	index4 atomic.Uint32
	index6 atomic.Uint32

	// registry tracks the live long-lived UDP sockets for re-binding on
	// uplink changes (marking machinery, sockmark_windows.go); netChangeCh
	// carries network-change notifications from the OS callbacks to the
	// watch goroutine, which debounces them.
	registry    sockRegistry
	netChangeCh chan struct{}

	mu         sync.Mutex
	routeState *routeState
	natState   *natState
}

// NewManager returns the Manager for Windows. The uplink indexes stay zero
// (marking is a no-op) until Start performs the initial detection.
func NewManager() *Manager {
	return &Manager{netChangeCh: make(chan struct{}, 1)}
}

// Start synchronously detects the current uplink and launches the
// network-change watcher, which lives until ctx is cancelled. An offline
// start (no default route → both indexes 0) is not an error: the watcher
// picks the uplink up when connectivity appears and re-binds registered
// sockets, so a restart is never needed. Only the change-notification
// registration itself can fail — and that deliberately fails Init (unlike
// the Linux Start, whose netlink monitor is best-effort staleness tracking):
// the notifications drive socket marking itself, which never recovers
// without them. Called before the first libp2p socket is created (Init
// guarantees this); the ordering matters mostly for TCP — a UDP socket
// created earlier is registered and re-bound by the initial uplink
// re-detection anyway, but a TCP dial made before Start would stay unmarked
// for its lifetime.
func (m *Manager) Start(ctx context.Context) error {
	m.redetectUplinks()

	routeCb, err := winipcfg.RegisterRouteChangeCallback(func(_ winipcfg.MibNotificationType, route *winipcfg.MibIPforwardRow2) {
		// Only default-route changes can change the uplink choice.
		if route != nil && route.DestinationPrefix.PrefixLength == 0 {
			m.notifyNetChange()
		}
	})
	if err != nil {
		return fmt.Errorf("register route change callback: %w", err)
	}
	ifaceCb, err := winipcfg.RegisterInterfaceChangeCallback(func(notificationType winipcfg.MibNotificationType, _ *winipcfg.MibIPInterfaceRow) {
		// Parameter changes cover interface metric flips, which reorder
		// default routes without touching the route table itself.
		if notificationType == winipcfg.MibParameterNotification {
			m.notifyNetChange()
		}
	})
	if err != nil {
		_ = routeCb.Unregister()
		return fmt.Errorf("register interface change callback: %w", err)
	}

	go m.watch(ctx, func() {
		_ = routeCb.Unregister()
		_ = ifaceCb.Unregister()
	})

	logger.Infof("socket marker started (IPv4 uplink ifIndex %d, IPv6 uplink ifIndex %d)",
		m.index4.Load(), m.index6.Load())
	return nil
}

// notifyNetChange signals the watch goroutine that the network changed, which
// reacts after a debounce (onNetworkChange). Non-blocking and coalescing:
// called from OS notification threads.
func (m *Manager) notifyNetChange() {
	select {
	case m.netChangeCh <- struct{}{}:
	default:
	}
}

// watch is the Manager's background goroutine: the debounced reaction to
// network change notifications plus the periodic registry sweep. cleanup
// unregisters the OS callbacks when ctx dies.
func (m *Manager) watch(ctx context.Context, cleanup func()) {
	defer cleanup()

	coalesce := time.NewTimer(time.Hour)
	if !coalesce.Stop() {
		<-coalesce.C
	}
	defer coalesce.Stop()
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()

	stopCoalesce := func() {
		if !coalesce.Stop() {
			select {
			case <-coalesce.C:
			default:
			}
		}
	}

	var burstStart time.Time
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.netChangeCh:
			now := time.Now()
			switch {
			case !pending:
				burstStart = now
				pending = true
				coalesce.Reset(debounceInterval)
			case now.Sub(burstStart) >= debounceBurstMax:
				// The burst has been going on too long — don't starve the
				// re-detection, run it now.
				stopCoalesce()
				pending = false
				m.onNetworkChange()
			default:
				stopCoalesce()
				coalesce.Reset(debounceInterval)
			}
		case <-coalesce.C:
			pending = false
			m.onNetworkChange()
		case <-sweep.C:
			if evicted := m.registry.sweep(); evicted > 0 {
				logger.Debugf("registry sweep evicted %d closed sockets", evicted)
			}
		}
	}
}

// onNetworkChange runs the debounced consumers of a network-change event:
// socket-marking re-detection first (lock-free — atomics + the registry's own
// lock), then the exit node's forwarding re-sync, which takes m.mu.
// EnableServerNAT can hold m.mu for seconds of PowerShell, and must never
// delay socket re-binding — only the re-sync itself, which is harmless.
func (m *Manager) onNetworkChange() {
	m.redetectUplinks()
	m.resyncServerForwarding()
}

// EnableClientRoutes installs the gateway client routes on the TUN (the
// default-route capture plus the IPv6 fail-closed fence). Idempotent: a
// second call while routes are installed is a no-op. Offline enable is
// allowed with a warning: the /1 routes are bound to the TUN LUID and need
// no uplink, marking with index 0 is a no-op, and the watcher re-binds
// registered sockets once connectivity appears — the gateway self-heals
// without a re-enable. IPv6 is not required: the tunnel is IPv4-only and
// gateway mode fences IPv6.
func (m *Manager) EnableClientRoutes(tunIfName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.routeState != nil {
		return nil
	}
	if m.index4.Load() == 0 {
		logger.Warnf("gateway client enabled with no IPv4 uplink; internet will flow when network appears")
	}

	state, err := m.setupGatewayRoutes(tunIfName)
	if err != nil {
		return fmt.Errorf("setup gateway routes: %w", err)
	}
	m.routeState = state
	return nil
}

// DisableClientRoutes removes the gateway client routes. Idempotent; a no-op
// when they are not installed. The state is dropped even if the OS teardown
// reports errors (partial leftovers die with the TUN adapter anyway), so a
// failure here never wedges the gateway in half-enabled.
func (m *Manager) DisableClientRoutes() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.routeState == nil {
		return nil
	}
	state := m.routeState
	m.routeState = nil
	return m.teardownGatewayRoutes(state)
}

// ClientRoutesActive reports whether gateway client routes are currently
// installed.
func (m *Manager) ClientRoutesActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routeState != nil
}

// EnableServerNAT configures the exit-node data path for the awl subnet
// (WFP filter + per-interface forwarding + WinNAT). Idempotent: a second
// call while NAT is configured is a no-op.
func (m *Manager) EnableServerNAT(awlSubnet, awlSubnet6, tunIfName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.natState != nil {
		return nil
	}
	state, err := m.setupNAT(awlSubnet, awlSubnet6, tunIfName)
	if err != nil {
		return fmt.Errorf("setup NAT: %w", err)
	}
	m.natState = state
	return nil
}

// DisableServerNAT reverses EnableServerNAT. Idempotent; a no-op when NAT is
// not configured. Like DisableClientRoutes, the state is dropped even on
// teardown errors (partial leftovers are recovered by the stale-cleanup on
// the next enable).
func (m *Manager) DisableServerNAT() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.natState == nil {
		return nil
	}
	state := m.natState
	m.natState = nil
	return m.teardownNAT(state)
}

// ServerNATActive reports whether the exit-node NAT is currently configured.
func (m *Manager) ServerNATActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.natState != nil
}
