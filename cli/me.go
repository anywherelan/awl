package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/olekukonko/tablewriter"

	"github.com/anywherelan/awl/api/apiclient"
)

func printStatus(api *apiclient.Client, w io.Writer) error {
	stats, err := api.PeerInfo()
	if err != nil {
		return err
	}

	table := tablewriter.NewWriter(w)
	table.AppendBulk([][]string{
		{"Download rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateIn, stats.NetworkStatsInIECUnits.TotalIn)},
		{"Upload rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateOut, stats.NetworkStatsInIECUnits.TotalOut)},
		{"Bootstrap peers", fmt.Sprintf("%d/%d", stats.ConnectedBootstrapPeers, stats.TotalBootstrapPeers)},
		{"DNS", formatWorkingStatus(stats.IsAwlDNSSetAsSystem)},
		{"SOCKS5 Proxy", formatWorkingStatus(stats.SOCKS5.ListenerEnabled)},
		{"SOCKS5 Proxy address", stats.SOCKS5.ListenAddress},
		{"SOCKS5 Proxy exit node", stats.SOCKS5.UsingPeerName},
		{"Reachability", strings.ToLower(stats.Reachability)},
		{"Uptime", stats.Uptime.Round(time.Second).String()},
		{"Server version", stats.ServerVersion},
	})

	table.Render()

	return nil
}

func formatWorkingStatus(working bool) string {
	if working {
		return "working"
	}
	return "not working"
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
