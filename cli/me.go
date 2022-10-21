package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/olekukonko/tablewriter"
)

func printStatus(api *apiclient.Client) error {
	stats, err := api.PeerInfo()
	if err != nil {
		return err
	}

	dnsStatus := "working"
	if !stats.IsAwlDNSSetAsSystem {
		dnsStatus = "not working"
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.AppendBulk([][]string{
		{"Download rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateIn, stats.NetworkStatsInIECUnits.TotalIn)},
		{"Upload rate", fmt.Sprintf("%s (%s)", stats.NetworkStatsInIECUnits.RateOut, stats.NetworkStatsInIECUnits.TotalOut)},
		{"Bootstrap peers", fmt.Sprintf("%d/%d", stats.TotalBootstrapPeers, stats.ConnectedBootstrapPeers)},
		{"DNS", dnsStatus},
		{"Reachability", strings.ToLower(stats.Reachability)},
		{"Uptime", stats.Uptime.Round(time.Second).String()},
		{"Server version", stats.ServerVersion},
	})

	table.Render()

	return nil
}

func printPeerId(api *apiclient.Client) error {
	stats, err := api.PeerInfo()
	if err != nil {
		return err
	}
	fmt.Printf("your peer id: %s", stats.PeerID)
	return nil
}
