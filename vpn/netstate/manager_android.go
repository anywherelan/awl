//go:build linux && android

package netstate

import (
	"context"
	"fmt"
	"sync"
	"syscall"
)

// ProtectFunc is the type of the callback supplied by the Android host
// application (VpnService.protect via the gomobile/JNI bridge). Returns true
// on success.
type ProtectFunc func(fd int) bool

// Manager on Android delegates everything to the host application: socket
// marking invokes the host-supplied protect callback for each libp2p socket
// so the host's VpnService.protect() can mark it as bypassing the TUN, and
// routes/NAT are owned by the host's VpnService.Builder — Enable*/Disable*
// only track the enabled state so Active reporting stays consistent with the
// other platforms.
//
// The protector is set once at construction (via NewAndroidManager). To
// change it, stop the application and start a new one with a fresh Manager.
// This avoids any runtime synchronisation and matches the gomobile-lib
// lifecycle (one Application instance per StartServer call).
type Manager struct {
	protect ProtectFunc

	mu                 sync.Mutex
	clientRoutesActive bool
	serverNATActive    bool
}

// NewManager returns a Manager with a no-op protector (ControlFunc will
// return nil). Production code should construct the Manager via
// NewAndroidManager with a protector wired to VpnService.protect; NewManager
// exists so that the cross-platform default construction path works and
// callers that don't enable gateway mode don't have to special-case Android.
func NewManager() *Manager {
	return &Manager{}
}

// NewAndroidManager returns a Manager whose socket marker calls the
// host-supplied protect callback (VpnService.protect via the gomobile/JNI
// bridge) for each new socket.
func NewAndroidManager(protect ProtectFunc) *Manager {
	return &Manager{protect: protect}
}

// Start is a no-op: socket protection is delegated to the host's
// VpnService.protect, which owns its own network tracking.
func (m *Manager) Start(_ context.Context) error { return nil }

// ControlFunc returns a function compatible with net.Dialer.Control that
// invokes the host-supplied protector for each new socket. Returns nil when
// no protector was supplied (NewManager).
func (m *Manager) ControlFunc() func(network, address string, c syscall.RawConn) error {
	if m.protect == nil {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			// gomobile turns Java-side exceptions into Go panics. If the
			// host VpnService has been destroyed between Application.Close
			// and a still-running libp2p dial, the JVM ref behind protect
			// may be dead; recover so the dial fails cleanly instead of
			// crashing the process.
			defer func() {
				if r := recover(); r != nil {
					logger.Warnf("VpnService.protect panicked for fd %d: %v", fd, r)
					sockErr = fmt.Errorf("VpnService.protect panic: %v", r)
				}
			}()
			if !m.protect(int(fd)) {
				sockErr = fmt.Errorf("VpnService.protect failed for fd %d", fd)
			}
		})
		if err != nil {
			return fmt.Errorf("sockmark control: %w", err)
		}
		return sockErr
	}
}

// EnableClientRoutes only records the enabled state: routes are configured
// via VpnService.Builder in the Android app:
//   - Gateway mode: builder.addRoute("0.0.0.0", 0) + builder.addRoute("::", 0)
//   - Normal mode: builder.addRoute("10.66.0.0", 24) (awl subnet only)
func (m *Manager) EnableClientRoutes(_ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clientRoutesActive = true
	return nil
}

// DisableClientRoutes only records the disabled state — see EnableClientRoutes.
func (m *Manager) DisableClientRoutes() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clientRoutesActive = false
	return nil
}

// ClientRoutesActive reports whether gateway client routes are enabled.
func (m *Manager) ClientRoutesActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clientRoutesActive
}

// EnableServerNAT only records the enabled state: Android exit node support
// requires root or special system configuration, so no OS state is touched.
func (m *Manager) EnableServerNAT(_, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverNATActive = true
	return nil
}

// DisableServerNAT only records the disabled state — see EnableServerNAT.
func (m *Manager) DisableServerNAT() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverNATActive = false
	return nil
}

// ServerNATActive reports whether the exit-node NAT is enabled.
func (m *Manager) ServerNATActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.serverNATActive
}
