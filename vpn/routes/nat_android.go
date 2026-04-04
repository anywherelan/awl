//go:build linux && android

package routes

// NATState holds the state needed to teardown NAT rules.
type NATState struct{}

// SetupNAT is a no-op on Android.
// Android exit node support requires root or special system configuration.
func SetupNAT(awlSubnet, tunIfName string) (*NATState, error) {
	return &NATState{}, nil
}

// TeardownNAT is a no-op on Android.
func TeardownNAT(state *NATState) error {
	return nil
}
