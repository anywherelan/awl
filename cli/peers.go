package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/entity"
)

func printPeersStatus(api *apiclient.Client, format string) error {
	const (
		TableFormatRowNumber    = "n"
		TableFormatPeer         = "p"
		TableFormatPeerID       = "i"
		TableFormatStatus       = "s"
		TableFormatLastSeen     = "l"
		TableFormatNetworkUsage = "u"
		TableFormatConnection   = "c"
		TableFormatVersion      = "v"
		TableFormatExitNode     = "e"
	)

	fHeaderMap := map[string]string{
		TableFormatRowNumber:    "№",
		TableFormatPeer:         "peer",
		TableFormatPeerID:       "peer ID",
		TableFormatStatus:       "status",
		TableFormatLastSeen:     "last seen",
		TableFormatNetworkUsage: "network usage\n(↓in/↑out)",
		TableFormatConnection:   "connections\naddress | protocol",
		TableFormatVersion:      "version",
		TableFormatExitNode:     "exit node",
	}

	if len(format) < 1 {
		return fmt.Errorf("format flag is incorrect: format should contain at leest 1 char")
	}

	table := tablewriter.NewWriter(os.Stdout)

	headers := make([]string, 0, len(format))
	columns := make([]string, 0, len(format))
	for ci, fc := range format {
		fcs := string(fc)
		if _, ok := fHeaderMap[fcs]; !ok {
			return fmt.Errorf("format flag is incorrect: unknown format flat char \"%s\"", fcs)
		}

		if fcs == TableFormatRowNumber {
			// BUG! lib expand empty cells to min width 2. We make our filed cells the same size with empty
			table.SetColMinWidth(ci, 2)
		}
		headers = append(headers, fHeaderMap[fcs])
		columns = append(columns, fcs)
	}

	peers, err := api.KnownPeers()
	if err != nil {
		return err
	}

	table.SetBorders(tablewriter.Border{Left: false, Top: false, Right: false, Bottom: false})
	table.SetRowLine(true)
	table.SetHeader(headers)
	for i, peer := range peers {
		row := make([]string, 0, len(columns))
		for _, col := range columns {
			switch col {
			case TableFormatRowNumber:
				row = append(row, strconv.Itoa(i+1))
			case TableFormatPeer:
				info := make([]string, 0, 3)
				if peer.DisplayName != "" {
					info = append(info, peer.DisplayName)
				}
				if peer.DomainName != "" {
					info = append(info, fmt.Sprintf("%s.%s", peer.DomainName, awldns.LocalDomain))
				}
				info = append(info, peer.IpAddr)

				row = append(row, strings.Join(info, "\n"))
			case TableFormatPeerID:
				row = append(row, peer.PeerID)
			case TableFormatStatus:
				status := "offline"
				if peer.Connected {
					status = "online"
				}
				if !peer.Confirmed {
					status += "\n(not confirmed)"
				}
				row = append(row, status)
			case TableFormatLastSeen:
				if peer.LastSeen.IsZero() {
					row = append(row, "never")
					break
				}
				row = append(row, peer.LastSeen.Format("2006-01-02\n15:04:05"))
			case TableFormatNetworkUsage:
				row = append(row,
					fmt.Sprintf("↓ %s (%s)\n↑ %s (%s)",
						peer.NetworkStatsInIECUnits.RateIn,
						peer.NetworkStatsInIECUnits.TotalIn,
						peer.NetworkStatsInIECUnits.RateOut,
						peer.NetworkStatsInIECUnits.TotalOut,
					),
				)
			case TableFormatConnection:
				consStr := make([]string, 0, len(peer.Connections))
				for _, con := range peer.Connections {
					if con.ThroughRelay {
						consStr = append(consStr, "through relay")
						continue
					}
					consStr = append(consStr, fmt.Sprintf("%s | %s", con.Address, con.Protocol))
				}
				row = append(row, strings.Join(consStr, "\n"))
			case TableFormatVersion:
				row = append(row, peer.Version)
			case TableFormatExitNode:
				row = append(row, fmt.Sprintf("we allow:     %v\npeer allowed: %v", peer.WeAllowUsingAsExitNode, peer.AllowedUsingAsExitNode))
			}
		}
		table.Append(row)
	}
	table.Render()
	return nil
}

func printFriendRequests(api *apiclient.Client) error {
	authRequests, err := api.AuthRequests()
	if err != nil {
		return err
	}
	if len(authRequests) == 0 {
		fmt.Println("you have no incoming requests")
		return nil
	}
	for _, req := range authRequests {
		fmt.Printf("Name: '%s' peerID: %s\n", req.Name, req.PeerID)
	}

	return nil
}

func getPeerIdByAlias(api *apiclient.Client, alias string) (string, error) {
	if alias == "" {
		return "", errors.New("name is empty")
	}

	peers, err := api.KnownPeers()
	if err != nil {
		return "", err
	}

	for _, p := range peers {
		if p.Alias == alias {
			return p.PeerID, nil
		}
	}
	return "", fmt.Errorf("can't find peer with name \"%s\"", alias)
}

func addPeer(api *apiclient.Client, peerID, alias string) error {
	authRequests, err := api.AuthRequests()
	if err != nil {
		return err
	}
	hasRequest := false
	for _, req := range authRequests {
		if req.PeerID == peerID {
			hasRequest = true
			break
		}
	}
	if hasRequest {
		err := api.ReplyFriendRequest(peerID, alias, false)
		if err != nil {
			return err
		}

		fmt.Println("user added to friends list successfully")
		return nil
	}

	err = api.SendFriendRequest(peerID, alias)
	if err != nil {
		return err
	}
	fmt.Println("friend request sent successfully")
	return nil
}

func removePeer(api *apiclient.Client, peerID string) error {
	err := api.RemovePeer(peerID)
	if err != nil {
		return err
	}

	fmt.Println("peer removed successfully")
	return nil
}

func changePeerAlias(api *apiclient.Client, peerID, newAlias string) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                newAlias,
		DomainName:           pcfg.DomainName,
		AllowUsingAsExitNode: pcfg.WeAllowUsingAsExitNode,
	})
	if err != nil {
		return err
	}

	fmt.Println("peer name updated successfully")
	return nil
}

func changePeerDomain(api *apiclient.Client, peerID, newDomain string) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                pcfg.Alias,
		DomainName:           newDomain,
		AllowUsingAsExitNode: pcfg.WeAllowUsingAsExitNode,
	})
	if err != nil {
		return err
	}

	fmt.Println("peer domain name updated successfully")
	return nil
}

func setAllowUsingAsExitNode(api *apiclient.Client, peerID string, allow bool) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                pcfg.Alias,
		DomainName:           pcfg.DomainName,
		AllowUsingAsExitNode: allow,
	})
	if err != nil {
		return err
	}

	fmt.Println("AllowUsingAsExitNode config updated successfully")
	return nil
}
