//go:build !linux && !windows

package routes

import "errors"

// NATState holds the state needed to teardown NAT rules.
type NATState struct{}

// SetupNAT is not supported on this platform.
func SetupNAT(awlSubnet, tunIfName string) (*NATState, error) {
	return nil, errors.New("NAT setup not supported on this platform")
}

// TeardownNAT is not supported on this platform.
func TeardownNAT(state *NATState) error {
	return nil
}
