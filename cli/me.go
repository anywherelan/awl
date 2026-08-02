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
		{"Name", stats.Name},
		{"Download rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateIn, stats.NetworkStatsInIECUnits.TotalIn)},
		{"Upload rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateOut, stats.NetworkStatsInIECUnits.TotalOut)},
		{"Bootstrap peers", fmt.Sprintf("%d/%d", stats.ConnectedBootstrapPeers, stats.TotalBootstrapPeers)},
		{"VPN", formatVPNStatus(stats.VPN)},
		{"VPN gateway client", formatVPNGatewayClient(stats.VPNGateway)},
	}
	gw := stats.VPNGateway
	if gw.ClientEnabled {
		if detail := formatExitDetail(gw.GatewayPublicIP, gw.GatewayPing, gw.Connected, gw.GatewayThroughRelay); detail != "" {
			rows = append(rows, []string{"VPN gateway exit", detail})
		}
	}
	rows = append(rows,
		[]string{"VPN gateway server", formatWorkingStatus(stats.VPNGateway.ServerEnabled)},
		[]string{"SOCKS5 Proxy", formatServiceStatus(stats.SOCKS5.ListenerEnabled, stats.SOCKS5.ListenAddress)},
	)

	s5 := stats.SOCKS5
	if exit := formatSOCKS5ExitNode(s5); exit != "off" {
		rows = append(rows, []string{"SOCKS5 exit node", exit})
		if detail := formatExitDetail(s5.UsingPeerPublicIP, s5.UsingPeerPing, s5.Connected, s5.UsingPeerThroughRelay); detail != "" {
			rows = append(rows, []string{"SOCKS5 exit", detail})
		}
	}

	rows = append(rows,
		[]string{"DNS", formatServiceStatus(stats.IsAwlDNSSetAsSystem, stats.AwlDNSAddress)},
		[]string{"Reachability", strings.ToLower(stats.Reachability)},
		[]string{"Uptime", formatUptime(stats.Uptime)},
		[]string{"Server version", stats.ServerVersion},
	)

	table := tablewriter.NewWriter(w)
	table.SetAutoWrapText(false)
	table.AppendBulk(rows)

	table.Render()

	return nil
}

const (
	statusWorking    = "working"
	statusNotWorking = "not working"
)

func formatWorkingStatus(working bool) string {
	if working {
		return statusWorking
	}
	return statusNotWorking
}

// formatEnabled renders an on/off feature toggle.
func formatEnabled(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// formatConnected renders libp2p connectivity to a peer.
func formatConnected(connected bool) string {
	if connected {
		return "connected"
	}
	return "disconnected"
}

// formatServiceStatus renders an enabled/disabled service and, when enabled,
// appends its listen address in parentheses, e.g. "working (127.0.0.66:53)".
// The address is omitted while the service is off.
func formatServiceStatus(enabled bool, addr string) string {
	if !enabled {
		return statusNotWorking
	}
	if addr != "" {
		return fmt.Sprintf("%s (%s)", statusWorking, addr)
	}
	return statusWorking
}

// formatVPNStatus renders the local VPN interface state, including the
// interface name and assigned address when it is up, e.g.
// "working (awl0, 10.66.0.1/24)".
func formatVPNStatus(vpn entity.VPNInfo) string {
	if !vpn.VPNInterfaceEnabled {
		return statusNotWorking
	}
	var parts []string
	if vpn.InterfaceName != "" {
		parts = append(parts, vpn.InterfaceName)
	}
	if vpn.IPNet != "" {
		parts = append(parts, vpn.IPNet)
	}
	if len(parts) == 0 {
		return statusWorking
	}
	return fmt.Sprintf("%s (%s)", statusWorking, strings.Join(parts, ", "))
}

// formatExitDetail renders a compact one-line summary of a selected exit peer
// (SOCKS5 or VPN gateway): "<public IP>, ping <N>ms, direct/via relay". Each
// part is included only when meaningful; the relay part appears only while
// connected. Returns "" when nothing is known.
func formatExitDetail(publicIP string, ping time.Duration, connected, throughRelay bool) string {
	var parts []string
	if publicIP != "" {
		parts = append(parts, publicIP)
	}
	if ping > 0 {
		parts = append(parts, "ping "+ping.Round(time.Millisecond).String())
	}
	if connected {
		parts = append(parts, formatRelay(throughRelay))
	}
	return strings.Join(parts, ", ")
}

// formatRelay renders the connection path of an exit peer in human terms.
func formatRelay(throughRelay bool) string {
	if throughRelay {
		return "via relay"
	}
	return "direct"
}

// formatUptime renders uptime with spaces between units. For 24h or more it
// switches to day granularity and drops seconds (e.g. "1d 21h 13m"); below 24h
// it keeps seconds and omits leading zero units (e.g. "1h 25m 5s", "5s").
func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	d -= mins * time.Minute
	secs := d / time.Second

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", int64(days), int64(hours), int64(mins))
	}

	var parts []string
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", int64(hours)))
	}
	if mins > 0 || hours > 0 {
		parts = append(parts, fmt.Sprintf("%dm", int64(mins)))
	}
	parts = append(parts, fmt.Sprintf("%ds", int64(secs)))
	return strings.Join(parts, " ")
}

func formatSOCKS5ExitNode(s5 entity.SOCKS5Info) string {
	return formatExitClient(s5.UsingPeerID != "", s5.Connected, s5.UsingPeerName, s5.UsingPeerID)
}

func formatVPNGatewayClient(gw entity.VPNGatewayInfo) string {
	return formatExitClient(gw.ClientEnabled, gw.Connected, gw.GatewayPeerName, gw.GatewayPeerID)
}

func formatExitClient(enabled, connected bool, peerName, peerID string) string {
	if !enabled {
		return "off"
	}
	name := peerName
	if name == "" {
		name = peerID
	}
	return fmt.Sprintf("%s [%s]", name, formatConnected(connected))
}

func printPeerId(api *apiclient.Client, w io.Writer) error {
	info, err := api.PeerInfo()
	if err != nil {
		return err
	}

	// A link without a token: it identifies us and carries our name, so the
	// other side can fill the add form from a single scan, but it grants
	// nothing on its own — that is what `peers invite create` is for.
	link := entity.BuildInviteLink(info.PeerID, "", info.Name)

	fmt.Fprintf(w, "your peer id: %s\n", info.PeerID)
	fmt.Fprintf(w, "your link:    %s\n", link)

	qrterminal.GenerateHalfBlock(link, qrterminal.M, w)

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
