package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/olekukonko/tablewriter"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/entity"
)

func printStatus(api *apiclient.Client, w io.Writer) error {
	stats, err := api.PeerInfo()
	if err != nil {
		return err
	}

	rows := [][]string{
		{"Download rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateIn, stats.NetworkStatsInIECUnits.TotalIn)},
		{"Upload rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateOut, stats.NetworkStatsInIECUnits.TotalOut)},
		{"Bootstrap peers", fmt.Sprintf("%d/%d", stats.ConnectedBootstrapPeers, stats.TotalBootstrapPeers)},
		{"DNS", formatWorkingStatus(stats.IsAwlDNSSetAsSystem)},
		{"SOCKS5 Proxy", formatWorkingStatus(stats.SOCKS5.ListenerEnabled)},
		{"SOCKS5 Proxy address", stats.SOCKS5.ListenAddress},
		{"SOCKS5 Proxy exit node", stats.SOCKS5.UsingPeerName},
		{"VPN gateway client", formatVPNGatewayClient(stats.VPNGateway)},
	}
	gw := stats.VPNGateway
	if gw.ClientEnabled {
		if gw.GatewayPublicIP != "" {
			rows = append(rows, []string{"VPN gateway public IP", gw.GatewayPublicIP})
		}
		if gw.GatewayPing > 0 {
			rows = append(rows, []string{"VPN gateway ping", gw.GatewayPing.Round(time.Millisecond).String()})
		}
		rows = append(rows, []string{"VPN gateway via relay", fmt.Sprintf("%v", gw.GatewayThroughRelay)})
	}
	rows = append(rows,
		[]string{"VPN gateway server", formatWorkingStatus(stats.VPNGateway.ServerEnabled)},
		[]string{"Reachability", strings.ToLower(stats.Reachability)},
		[]string{"Uptime", stats.Uptime.Round(time.Second).String()},
		[]string{"Server version", stats.ServerVersion},
	)

	table := tablewriter.NewWriter(w)
	table.AppendBulk(rows)

	table.Render()

	return nil
}

func formatWorkingStatus(working bool) string {
	if working {
		return "working"
	}
	return "not working"
}

// formatVPNGatewayClient renders the client-side VPN gateway state in a
// single line: either "off", or the gateway peer's name + connectivity.
// Mirrors how SOCKS5 status splits across two rows but compresses to one
// since VPN gateway has fewer knobs to surface.
func formatVPNGatewayClient(gw entity.VPNGatewayInfo) string {
	if !gw.ClientEnabled {
		return "off"
	}
	name := gw.GatewayPeerName
	if name == "" {
		name = gw.GatewayPeerID
	}
	conn := "disconnected"
	if gw.Connected {
		conn = "connected"
	}
	return fmt.Sprintf("via %s [%s]", name, conn)
}

func printPeerId(api *apiclient.Client, w io.Writer) error {
	info, err := api.PeerInfo()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "your peer id: %s\n", info.PeerID)

	qrterminal.GenerateHalfBlock(info.PeerID, qrterminal.M, w)

	return nil
}

func renameMe(api *apiclient.Client, newName string, w io.Writer) error {
	err := api.UpdateMySettings(newName)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "my peer name updated successfully")

	return nil
}

func listProxies(api *apiclient.Client, w io.Writer) error {
	proxies, err := api.ListAvailableProxies()
	if err != nil {
		return err
	}

	if len(proxies) == 0 {
		fmt.Fprintln(w, "no available proxies")
		return nil
	}

	fmt.Fprintln(w, "Proxies:")
	for _, proxy := range proxies {
		fmt.Fprintf(w, "- peer name: %s | peer id: %s\n", proxy.PeerName, proxy.PeerID)
	}

	return nil
}

func setProxy(api *apiclient.Client, peerID string, w io.Writer) error {
	err := api.UpdateProxySettings(peerID)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "proxy settings updated successfully")

	return nil
}
