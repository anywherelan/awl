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

	line := func(label, value string) {
		fmt.Fprintf(w, "%-14s %s\n", label+":", value)
	}

	line("Server", formatEnabled(gw.ServerEnabled))
	line("Client", formatEnabled(gw.ClientEnabled))
	if gw.ClientEnabled {
		name := gw.GatewayPeerName
		if name == "" {
			name = gw.GatewayPeerID
		}
		line("Gateway peer", fmt.Sprintf("%s [%s]", name, formatConnected(gw.Connected)))
		line("Peer ID", gw.GatewayPeerID)
		if gw.GatewayPublicIP != "" {
			line("Public IP", gw.GatewayPublicIP)
		}
		if gw.GatewayPing > 0 {
			line("Ping", gw.GatewayPing.Round(time.Millisecond).String())
		}
		if gw.Connected {
			line("Connection", formatRelay(gw.GatewayThroughRelay))
		}
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
