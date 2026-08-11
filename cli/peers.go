package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/entity"
)

func printPeersStatus(api *apiclient.Client, format string, w io.Writer) error {
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

	table := tablewriter.NewWriter(w)

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
				if peer.IpAddrV6 != "" {
					info = append(info, peer.IpAddrV6)
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

func printFriendRequests(api *apiclient.Client, w io.Writer) error {
	authRequests, err := api.AuthRequests()
	if err != nil {
		return err
	}
	if len(authRequests) == 0 {
		fmt.Fprintln(w, "you have no incoming requests")
		return nil
	}
	for _, req := range authRequests {
		fmt.Fprintf(w, "Name: '%s' peerID: %s suggestedIP: %s\n", req.Name, req.PeerID, req.SuggestedIP)
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

// addPeerParams is the flags of `peers add`. A peer is named either by an
// invite link or by a peer id; the rest is optional.
type addPeerParams struct {
	// Link is an invite link. With a token in it the peer auto accepts us.
	// Without a token, link is merely a peer id with a name, and the remote peer needs to accept us.
	Link                 string
	PeerID               string
	Alias                string
	IPAddr               string
	AllowUsingAsExitNode bool
	// token is resolved from Link
	token string
}

func addPeer(api *apiclient.Client, params addPeerParams, w io.Writer) error {
	params, err := resolveAddPeerTarget(params)
	if err != nil {
		return err
	}

	authRequests, err := api.AuthRequests()
	if err != nil {
		return err
	}
	hasRequest := false
	for _, req := range authRequests {
		if req.PeerID == params.PeerID {
			hasRequest = true
			break
		}
	}
	if hasRequest {
		// Drop the token on purpose: a peer that already sent us a request
		// already knows us, so FriendRequestReply intentionally carries no token.
		err := api.ReplyFriendRequest(entity.FriendRequestReply{
			PeerID:               params.PeerID,
			Alias:                params.Alias,
			IPAddr:               params.IPAddr,
			AllowUsingAsExitNode: params.AllowUsingAsExitNode,
		})
		if err != nil {
			return err
		}

		fmt.Fprintf(w, "successfully accepted existing invitation from the device '%s'\n", params.Alias)
		return nil
	}

	err = api.SendFriendRequest(entity.FriendRequest{
		PeerID:               params.PeerID,
		Alias:                params.Alias,
		IPAddr:               params.IPAddr,
		AllowUsingAsExitNode: params.AllowUsingAsExitNode,
		Token:                params.token,
	})
	if err != nil {
		return err
	}

	if params.token != "" {
		fmt.Fprintln(w, "friend request sent with the invite link; the peer accepts it automatically once it is online")
		return nil
	}
	fmt.Fprintln(w, "friend request sent successfully")
	return nil
}

// resolveAddPeerTarget validate params and resolves an invite link.
func resolveAddPeerTarget(params addPeerParams) (addPeerParams, error) {
	params.Alias = strings.TrimSpace(params.Alias)

	if params.Link != "" && params.PeerID != "" {
		return params, errors.New("link and pid flags are mutually exclusive: a link already has PeerID")
	}

	if params.Link != "" {
		link, err := entity.ParseInviteLink(params.Link)
		if err != nil {
			return params, err
		}
		params.token = link.Token
		params.PeerID = link.PeerID
		if params.Alias == "" {
			params.Alias = strings.TrimSpace(link.Name)
		}
	}

	switch {
	case params.PeerID == "":
		return params, errors.New("either link or pid flag is required")
	case params.Alias == "":
		return params, errors.New("name flag is required: the link carries no name to fall back on")
	}

	return params, nil
}

func removePeer(api *apiclient.Client, peerID string, w io.Writer) error {
	err := api.RemovePeer(peerID)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "peer removed successfully")
	return nil
}

func changePeerAlias(api *apiclient.Client, peerID, newAlias string, w io.Writer) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                newAlias,
		DomainName:           pcfg.DomainName,
		IPAddr:               pcfg.IPAddr,
		AllowUsingAsExitNode: pcfg.WeAllowUsingAsExitNode,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "peer name updated successfully")
	return nil
}

func changePeerDomain(api *apiclient.Client, peerID, newDomain string, w io.Writer) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                pcfg.Alias,
		DomainName:           newDomain,
		IPAddr:               pcfg.IPAddr,
		AllowUsingAsExitNode: pcfg.WeAllowUsingAsExitNode,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "peer domain name updated successfully")
	return nil
}

func changePeerIP(api *apiclient.Client, peerID, newIP string, w io.Writer) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                pcfg.Alias,
		DomainName:           pcfg.DomainName,
		IPAddr:               newIP,
		AllowUsingAsExitNode: pcfg.WeAllowUsingAsExitNode,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "peer IP address updated successfully")
	return nil
}

func setAllowUsingAsExitNode(api *apiclient.Client, peerID string, allow bool, w io.Writer) error {
	pcfg, err := api.KnownPeerConfig(peerID)
	if err != nil {
		return err
	}

	err = api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peerID,
		Alias:                pcfg.Alias,
		DomainName:           pcfg.DomainName,
		IPAddr:               pcfg.IPAddr,
		AllowUsingAsExitNode: allow,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "AllowUsingAsExitNode config updated successfully")
	return nil
}
