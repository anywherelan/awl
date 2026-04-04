//go:build windows

package routes

import (
	"fmt"
	"os/exec"
)

// NATState holds the state needed to teardown NAT on Windows.
type NATState struct {
	origForwarding string
}

// SetupNAT enables IP routing on Windows for exit node functionality.
// Full MASQUERADE-style NAT is not available via netsh; this only enables
// IP forwarding so that the exit node can route packets. It works when
// the exit node has a public/routable IP.
func SetupNAT(awlSubnet, tunIfName string) (*NATState, error) {
	state := &NATState{}

	// Save current forwarding state
	out, err := exec.Command("netsh", "interface", "ipv4", "show", "global").Output()
	if err != nil {
		return nil, fmt.Errorf("query ip forwarding: %w", err)
	}
	state.origForwarding = string(out)

	// Enable IP forwarding
	err = exec.Command("netsh", "interface", "ipv4", "set", "global", "forwarding=enabled").Run()
	if err != nil {
		return nil, fmt.Errorf("enable ip forwarding: %w", err)
	}

	return state, nil
}

// TeardownNAT disables IP routing on Windows.
func TeardownNAT(state *NATState) error {
	if state == nil {
		return nil
	}

	err := exec.Command("netsh", "interface", "ipv4", "set", "global", "forwarding=disabled").Run()
	if err != nil {
		return fmt.Errorf("disable ip forwarding: %w", err)
	}

	return nil
}
