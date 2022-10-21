package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/entity"
	"github.com/olekukonko/tablewriter"
)

func printPeersStatus(api *apiclient.Client, format string) error {
	const (
		TableFormatRowNumber  = "n"
		TableFormatPeer       = "p"
		TableFormatPeerID     = "i"
		TableFormatStatus     = "s"
		TableFormatDomain     = "d"
		TableFormatAddress    = "a"
		TableFormatLastSeen   = "l"
		TableFormatTotal      = "t"
		TableFormatRate       = "r"
		TableFormatConnection = "c"
		TableFormatVersion    = "v"
	)

	fHeaderMap := map[string]string{
		TableFormatRowNumber:  "№",
		TableFormatPeer:       "peer",
		TableFormatPeerID:     "peer ID",
		TableFormatStatus:     "status",
		TableFormatDomain:     "domain",
		TableFormatAddress:    "address",
		TableFormatLastSeen:   "last seen",
		TableFormatTotal:      "total\nin/out, B",
		TableFormatRate:       "rate\nin/out, B",
		TableFormatConnection: "connections\naddress | protocol",
		TableFormatVersion:    "version",
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

		switch fcs {
		case TableFormatRowNumber:
			table.SetColMinWidth(ci, 2) // BUG! lib expand empty cells to min width 2. We make our filed cells the same size with empty
		}
		headers = append(headers, fHeaderMap[fcs])
		columns = append(columns, fcs)
	}

	peers, err := api.KnownPeers()
	if err != nil {
		return err
	}

	table.SetBorders(tablewriter.Border{Left: false, Top: false, Right: false, Bottom: false})
	table.SetHeader(headers)
	for i, peer := range peers {
		row := make([]string, 0, len(columns))
		for _, col := range columns {
			switch col {
			case TableFormatRowNumber:
				row = append(row, strconv.Itoa(i+1))
			case TableFormatPeer:
				row = append(row, peer.DisplayName)
			case TableFormatPeerID:
				row = append(row, peer.PeerID)
			case TableFormatStatus:
				status := "disconnected"
				if peer.Connected {
					status = "connected"
				}
				if !peer.Confirmed {
					status += ", not confirmed"
				}
				row = append(row, status)
			case TableFormatDomain:
				row = append(row, peer.DomainName)
			case TableFormatAddress:
				row = append(row, peer.IpAddr)
			case TableFormatLastSeen:
				if peer.LastSeen.IsZero() {
					row = append(row, "never")
					break
				}
				row = append(row, peer.LastSeen.Format("2006-01-02 15:04:05"))
			case TableFormatTotal:
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
				for ci, con := range peer.Connections {
					if con.ThroughRelay {
						consStr = append(consStr, fmt.Sprintf("%d) through relay", ci+1))
						continue
					}
					consStr = append(consStr, fmt.Sprintf("%d) %s | %s", ci+1, con.Address, con.Protocol))
				}
				row = append(row, strings.Join(consStr, "\n"))
			case TableFormatVersion:
				row = append(row, peer.Version)
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
		fmt.Println("has no requests")
		return nil
	}
	for _, req := range authRequests {
		fmt.Printf("Name: '%s' peerID: %s\n", req.Name, req.PeerID)
	}

	return nil
}

func getPeerIdByAlias(api *apiclient.Client, alias string) (string, error) {
	if alias == "" {
		return "", errors.New("alias is empty")
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
	return "", fmt.Errorf("can't find peer with alias \"%s\"", alias)
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

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{PeerID: peerID, Alias: newAlias, DomainName: pcfg.DomainName})
	if err != nil {
		return err
	}

	fmt.Println("peer alias updated successfully")
	return nil
}
