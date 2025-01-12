package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/olekukonko/tablewriter"

	"github.com/anywherelan/awl/api/apiclient"
)

func printStatus(api *apiclient.Client) error {
	stats, err := api.PeerInfo()
	if err != nil {
		return err
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.AppendBulk([][]string{
		{"Download rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateIn, stats.NetworkStatsInIECUnits.TotalIn)},
		{"Upload rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateOut, stats.NetworkStatsInIECUnits.TotalOut)},
		{"Bootstrap peers", fmt.Sprintf("%d/%d", stats.TotalBootstrapPeers, stats.ConnectedBootstrapPeers)},
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

func printPeerId(api *apiclient.Client) error {
	info, err := api.PeerInfo()
	if err != nil {
		return err
	}
	fmt.Printf("your peer id: %s\n", info.PeerID)

	qrterminal.GenerateHalfBlock(info.PeerID, qrterminal.M, os.Stdout)

	return nil
}

func renameMe(api *apiclient.Client, newName string) error {
	err := api.UpdateMySettings(newName)
	if err != nil {
		return err
	}

	fmt.Println("my peer name updated successfully")

	return nil
}

func listProxies(api *apiclient.Client) error {
	proxies, err := api.ListAvailableProxies()
	if err != nil {
		return err
	}

	if len(proxies) == 0 {
		fmt.Println("no available proxies")
		return nil
	}

	fmt.Println("Proxies:")
	for _, proxy := range proxies {
		fmt.Printf("- peer name: %s | peer id: %s\n", proxy.PeerName, proxy.PeerID)
	}

	return nil
}

func setProxy(api *apiclient.Client, peerID string) error {
	err := api.UpdateProxySettings(peerID)
	if err != nil {
		return err
	}

	fmt.Println("proxy settings updated successfully")

	return nil
}
