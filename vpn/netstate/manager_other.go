//go:build !linux && !windows

package netstate

import (
	"context"
	"errors"
	"syscall"
)

// Manager is the no-op stub for platforms without VPN gateway support.
// application.go also blocks gateway mode on these platforms via
// setupGateway, so the Enable* errors should never be hit at runtime.
type Manager struct{}

// NewManager returns the no-op Manager for this platform.
func NewManager() *Manager {
	return &Manager{}
}

// Start is a no-op: there is no socket marking on this platform.
func (m *Manager) Start(_ context.Context) error { return nil }

// ControlFunc returns nil, disabling socket marking.
func (m *Manager) ControlFunc() func(network, address string, c syscall.RawConn) error {
	return nil
}

// EnableClientRoutes is not supported on this platform.
func (m *Manager) EnableClientRoutes(_ string) error {
	return errors.New("setup gateway routes: gateway routes not supported on this platform")
}

// DisableClientRoutes is a no-op on this platform.
func (m *Manager) DisableClientRoutes() error { return nil }

// ClientRoutesActive always reports false on this platform.
func (m *Manager) ClientRoutesActive() bool { return false }

// EnableServerNAT is not supported on this platform.
func (m *Manager) EnableServerNAT(_, _, _ string) error {
	return errors.New("setup NAT: NAT setup not supported on this platform")
}

// DisableServerNAT is a no-op on this platform.
func (m *Manager) DisableServerNAT() error { return nil }

// ServerNATActive always reports false on this platform.
func (m *Manager) ServerNATActive() bool { return false }
