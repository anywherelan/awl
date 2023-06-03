package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/mdp/qrterminal/v3"
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
