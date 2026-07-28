//go:build linux && !android

package netstate

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// awlMark is the numeric value shared by the SO_MARK fwmark applied to marked
// sockets and the policy-routing table ID holding their exemption routes.
// 0x61776C = "awl" in ASCII (lowercase). The two live in different kernel
// namespaces and don't collide; using one value makes awl-owned state
// trivially greppable in `ip rule` / `ip route show table`.
const awlMark = 0x61776C

// Manager owns the Linux OS network state behind AWL's VPN gateway feature:
// the always-on socket marking (SO_MARK, applied via ControlFunc), the
// runtime state of gateway mode — the client routes and the server NAT —
// and the route-change monitor (started by Start, alive for the whole
// process) that keeps the client routes' exemption table in sync with the
// live host defaults.
//
// Enable/Disable methods are idempotent and safe for concurrent use. The
// internal mutex only guards the Manager's own state; the orchestration
// above (service.VPNGateway) still serialises whole enable/disable
// transactions — config, tunnel binding, DNS and the calls here — under its
// own lock. The monitor's reconcile takes the same mutex, which is what
// makes it exclusive with Enable/Disable transitions.
type Manager struct {
	mu         sync.Mutex
	routeState *routeState
	natState   *natState

	// stopping suppresses the netlink subscription's ErrorCallback on
	// shutdown: closing our own live subscription socket (the monitor's
	// deferred close(subDone)) provokes a benign "Receive failed" that is
	// not worth a warning. Set by the monitor goroutine itself when ctx is
	// cancelled; never reset — ctx cancellation is terminal for the process.
	stopping atomic.Bool
}

// NewManager returns the Manager for Linux. Setting SO_MARK requires
// CAP_NET_ADMIN, which AWL already needs for TUN setup, so no extra
// capability is required.
func NewManager() *Manager {
	return &Manager{}
}

// Start launches the route-change monitor goroutine, which keeps the tableID
// exemption copies in sync with the live host default(s) for the whole
// process lifetime (see runRouteMonitor); it exits when ctx is cancelled.
// Socket marking itself needs no lifecycle here: SO_MARK is interpreted by
// the kernel per packet, so unlike Windows there is no per-socket state to
// keep in sync with the network.
//
// Always returns nil: the monitor is best-effort staleness tracking — the
// gateway works without it — so a subscribe failure is retried in the
// background forever rather than surfaced. (Contrast with Windows, where
// Start fails Init on a callback-registration error: there the notifications
// drive socket marking itself, which never recovers without them.)
func (m *Manager) Start(ctx context.Context) error {
	subDone := make(chan struct{})
	updates, err := m.subscribeRouteUpdates(subDone)
	if err != nil {
		// Hand the monitor an already-closed channel: its subscription-died
		// path fires immediately, entering the re-subscribe loop with backoff.
		logger.Warnf("gateway route monitor: initial subscribe failed, will keep retrying: %v", err)
		closed := make(chan netlink.RouteUpdate)
		close(closed)
		updates = closed
	}
	go m.runRouteMonitor(ctx, updates, subDone)
	return nil
}

// ControlFunc returns a function compatible with net.Dialer.Control and the
// QUIC ListenUDP override, marking each new socket with SO_MARK so its
// traffic bypasses the VPN tunnel via the fwmark ip rule.
func (m *Manager) ControlFunc() func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, awlMark)
		})
		if err != nil {
			return fmt.Errorf("sockmark control: %w", err)
		}
		if sockErr != nil {
			return fmt.Errorf("sockmark SO_MARK: %w", sockErr)
		}
		return nil
	}
}

// EnableClientRoutes installs the gateway client routes on the TUN (the
// default-route capture plus the IPv6 fail-closed fence). Idempotent: a
// second call while routes are installed is a no-op.
func (m *Manager) EnableClientRoutes(tunIfName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.routeState != nil {
		return nil
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
// reports errors (partial leftovers are recovered by the stale-cleanup on the
// next enable), so a failure here never wedges the gateway in half-enabled.
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
// (ip_forward + iptables chain + MASQUERADE). Idempotent: a second call while
// NAT is configured is a no-op.
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
// teardown errors.
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

// routeMonitorDebounce is the interval at which the monitor checks whether any
// relevant route event has arrived since the last reconcile. A single uplink
// change (DHCP renew, Wi-Fi roam, new RA) emits a burst of
// RTM_NEWROUTE/RTM_DELROUTE messages; the monitor only marks state dirty as they
// come in and reconciles at most once per tick, coalescing the burst into a
// single reconcile (bounded worst-case latency of one interval).
// Our reconcile is two netlink dumps + a small
// diff with no external consumers, so a short interval is fine — and the window
// it bounds is only degraded p2p, never a leak, so faster restoration is a mild
// plus.
const routeMonitorDebounce = 500 * time.Millisecond

// routeMonitorResubscribeBackoff is how long the monitor waits before retrying a
// netlink subscription that died mid-session (see runRouteMonitor).
const routeMonitorResubscribeBackoff = 1 * time.Second

// subscribeRouteUpdates opens a fresh netlink route-change subscription feeding a
// new updates channel. Closing subDone tears down THIS subscription's socket and
// its internal watcher goroutine — the library never closes the socket itself on
// a receive error, so each subscription needs its own cancel channel to avoid
// leaking a socket + goroutine per re-subscription. netlink closes the updates
// channel on any receive error, which the consumer treats as "subscription died".
// The ErrorCallback stays quiet once m.stopping is set: tearing our own socket
// down provokes a benign "Receive failed" that is not worth logging.
func (m *Manager) subscribeRouteUpdates(subDone <-chan struct{}) (chan netlink.RouteUpdate, error) {
	updates := make(chan netlink.RouteUpdate, 64)
	opts := netlink.RouteSubscribeOptions{
		ListExisting: false,
		ErrorCallback: func(err error) {
			if m.stopping.Load() {
				return
			}
			logger.Warnf("gateway route monitor: %v", err)
		},
	}
	if err := netlink.RouteSubscribeWithOptions(updates, subDone, opts); err != nil {
		return nil, err
	}
	return updates, nil
}

// runRouteMonitor consumes route-change events and reconciles tableID after a
// debounce window. It runs for the whole process lifetime and exits only when
// ctx is cancelled; a mid-session subscription death (netlink socket error →
// updates closed) is recovered by re-subscribing rather than giving up, so
// staleness tracking survives transient netlink failures. Events arriving
// while the gateway client is disabled only cost a dirty flag — reconcile
// bails out early on m.routeState == nil.
//
// subDone is the live subscription's cancel channel. It is closed (and replaced)
// on every re-subscription and once more on exit, so no dead subscription's
// socket or watcher goroutine outlives the event that killed it.
func (m *Manager) runRouteMonitor(ctx context.Context, updates <-chan netlink.RouteUpdate, subDone chan struct{}) {
	// Tears down whichever subscription is live when we exit. The closure reads
	// the current subDone, which the loop reassigns on each re-subscription.
	defer func() { close(subDone) }()

	ticker := time.NewTicker(routeMonitorDebounce)
	defer ticker.Stop()

	dirty := false
	for {
		select {
		case upd, ok := <-updates:
			if !ok {
				// Subscription died mid-session (or the initial subscribe in
				// Start failed). Release the dead subscription's socket +
				// watcher goroutine, then re-subscribe on a fresh socket and
				// force a catch-up reconcile (events may have been missed while
				// it was down). Bail only if we were stopped during backoff.
				close(subDone)
				subDone = make(chan struct{})
				newUpdates, ok := m.resubscribe(ctx, subDone)
				if !ok {
					return
				}
				updates = newUpdates
				dirty = true
				continue
			}
			if isDefaultRouteUpdate(upd) {
				dirty = true
			}
		case <-ticker.C:
			if dirty {
				dirty = false
				m.reconcile()
			}
		case <-ctx.Done():
			// Set before the deferred close(subDone), which tears down the
			// live socket and provokes the benign "Receive failed" that
			// stopping suppresses in the ErrorCallback.
			m.stopping.Store(true)
			return
		}
	}
}

// resubscribe retries the netlink subscription after a subscription death, with
// a backoff between attempts. It returns the new updates channel, or ok=false if
// ctx was cancelled (shutdown) while waiting/retrying. All attempts share
// subDone: a failed subscribe binds nothing, so reuse leaks nothing, and the one
// that succeeds binds its socket to subDone for the caller to cancel later.
func (m *Manager) resubscribe(ctx context.Context, subDone <-chan struct{}) (<-chan netlink.RouteUpdate, bool) {
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(routeMonitorResubscribeBackoff):
		}

		updates, err := m.subscribeRouteUpdates(subDone)
		if err != nil {
			logger.Warnf("gateway route monitor: re-subscribe failed, retrying in %s: %v", routeMonitorResubscribeBackoff, err)
			continue
		}
		logger.Infof("gateway route monitor: re-subscribed after subscription loss")
		return updates, true
	}
}

// isDefaultRouteUpdate reports whether a route event concerns a main-table
// default route (either family). Changes in other tables — including awl's own
// tableID edits — are ignored, so reconcile never triggers itself.
func isDefaultRouteUpdate(upd netlink.RouteUpdate) bool {
	r := upd.Route
	if r.Table != unix.RT_TABLE_MAIN {
		return false
	}
	return isIPv4DefaultDst(r.Dst) || isIPv6DefaultDst(r.Dst)
}
