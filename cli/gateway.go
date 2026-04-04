package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/anywherelan/awl/api/apiclient"
)

func gatewayStatus(api *apiclient.Client, w io.Writer) error {
	info, err := api.PeerInfo()
	if err != nil {
		return err
	}
	gw := info.VPNGateway

	fmt.Fprintf(w, "VPN gateway client enabled: %v\n", gw.ClientEnabled)
	fmt.Fprintf(w, "VPN gateway server enabled: %v\n", gw.ServerEnabled)
	if gw.ClientEnabled {
		fmt.Fprintf(w, "Gateway peer:               %s (%s)\n", gw.GatewayPeerName, gw.GatewayPeerID)
		fmt.Fprintf(w, "Gateway peer connected:     %v\n", gw.Connected)
		if gw.GatewayPublicIP != "" {
			fmt.Fprintf(w, "Gateway public IP:          %s\n", gw.GatewayPublicIP)
		}
		if gw.GatewayPing > 0 {
			fmt.Fprintf(w, "Gateway ping:               %s\n", gw.GatewayPing.Round(time.Millisecond))
		}
		fmt.Fprintf(w, "Gateway via relay:          %v\n", gw.GatewayThroughRelay)
	}

	return nil
}

func gatewaySetServerEnabled(api *apiclient.Client, enabled bool, w io.Writer) error {
	if err := api.SetVPNGatewayServerEnabled(enabled); err != nil {
		return err
	}
	if enabled {
		fmt.Fprintln(w, "VPN gateway server enabled")
	} else {
		fmt.Fprintln(w, "VPN gateway server disabled")
	}
	return nil
}

func gatewayClientUse(api *apiclient.Client, peerID string, w io.Writer) error {
	if err := api.EnableVPNGatewayClient(peerID); err != nil {
		return err
	}

	info, err := api.PeerInfo()
	if err != nil {
		return err
	}
	gw := info.VPNGateway
	name := gw.GatewayPeerName
	if name == "" {
		name = gw.GatewayPeerID
	}
	fmt.Fprintf(w, "VPN gateway client enabled, routing via %s (%s)\n", name, gw.GatewayPeerID)
	return nil
}

func gatewayClientStop(api *apiclient.Client, w io.Writer) error {
	if err := api.DisableVPNGatewayClient(); err != nil {
		return err
	}

	fmt.Fprintln(w, "VPN gateway client disabled")
	return nil
}

func gatewayList(api *apiclient.Client, w io.Writer) error {
	gateways, err := api.ListAvailableVPNGateways()
	if err != nil {
		return err
	}

	if len(gateways) == 0 {
		fmt.Fprintln(w, "no available VPN gateways (no devices with gateway server enabled, or status not yet exchanged)")
		return nil
	}

	fmt.Fprintln(w, "Available VPN gateways:")
	for _, gw := range gateways {
		connStatus := "disconnected"
		if gw.Connected {
			connStatus = "connected"
		}
		fmt.Fprintf(w, "- %s (%s) [%s]\n", gw.PeerName, gw.PeerID, connStatus)
	}

	return nil
}
