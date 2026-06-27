//go:build !linux && !windows

package routes

import "errors"

// RouteState holds the state needed to teardown gateway routes.
type RouteState struct{}

// SetupGatewayRoutes is not supported on this platform.
func SetupGatewayRoutes(tunIfName string, fwmark uint32) (*RouteState, error) {
	return nil, errors.New("gateway routes not supported on this platform")
}

// TeardownGatewayRoutes is not supported on this platform.
func TeardownGatewayRoutes(state *RouteState) error {
	return nil
}
