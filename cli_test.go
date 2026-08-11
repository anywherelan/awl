package awl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/anywherelan/awl/cli"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
)

// runCLIAddr runs a CLI command against the given API address and returns stdout output.
func runCLIAddr(addr string, args ...string) (string, error) {
	app := cli.New(config.AppTypeAwl)
	var buf bytes.Buffer
	fullArgs := append([]string{"cli", "--api_addr", addr}, args...)
	err := app.RunWithWriter(fullArgs, &buf)
	return buf.String(), err
}

// runCLI runs CLI commands against a running TestPeer daemon and returns output.
func runCLI(_ *TestSuite, peer TestPeer, args ...string) (string, error) {
	return runCLIAddr(peer.app.Api.Address(), args...)
}

// TestCLI_Me covers all "me" subcommands using a single shared peer.
// Rename runs last because it mutates the peer name.
func TestCLI_Me(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)

	t.Run("Status", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "me", "status")
		require.NoError(t, err)
		// Row labels are static; values are dynamic (uptime, bootstrap peers, reachability)
		for _, label := range []string{
			"Name", "Download rate", "Upload rate", "Bootstrap peers",
			"VPN", "DNS", "SOCKS5 Proxy",
			"VPN gateway client", "VPN gateway server",
			"Reachability", "Uptime", "Server version",
		} {
			require.Contains(t, out, label)
		}
		require.Contains(t, out, "not working") // DNS disabled in test config
		require.Contains(t, out, "working")     // SOCKS5 enabled in test config
		require.Contains(t, out, "off")         // VPN gateway client off by default
		require.Contains(t, out, "dev")         // config.Version in tests
	})

	t.Run("Id", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "me", "id")
		require.NoError(t, err)
		lines := strings.SplitN(strings.TrimRight(out, "\n"), "\n", 3)
		require.Len(t, lines, 3)
		require.Equal(t, fmt.Sprintf("your peer id: %s", peer1.PeerID()), lines[0])

		// The link is the shareable form of the same thing: it carries our name
		// and grants nothing, so it is safe to show next to the peer id.
		rawLink := strings.TrimSpace(strings.TrimPrefix(lines[1], "your link:"))
		link, err := entity.ParseInviteLink(rawLink)
		require.NoError(t, err)
		require.Equal(t, peer1.PeerID(), link.PeerID)
		require.Empty(t, link.Token, "`me id` must never print a token")

		require.NotEmpty(t, lines[2]) // QR code block follows
	})

	t.Run("ListProxies_Empty", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "me", "list_proxies")
		require.NoError(t, err)
		require.Equal(t, "no available proxies\n", out)
	})

	t.Run("Logs", func(t *testing.T) {
		// Trigger some log entries via API calls
		for range 3 {
			_, _ = peer1.api.PeerInfo()
		}
		out, err := runCLI(ts, peer1, "logs", "--n", "0")
		require.NoError(t, err)
		require.NotEmpty(t, strings.TrimSpace(out))
		require.Contains(t, out, "INFO") // log lines contain a level indicator
	})

	t.Run("P2pInfo", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "p2p_info")
		require.NoError(t, err)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &result))
		require.Contains(t, result, "General")
		require.Contains(t, result, "Connections")
	})

	t.Run("Rename", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "me", "rename", "--name", "new-test-name")
		require.NoError(t, err)
		require.Equal(t, "my peer name updated successfully\n", out)
		info, err := peer1.api.PeerInfo()
		require.NoError(t, err)
		require.Equal(t, "new-test-name", info.Name)
	})
}

// TestCLI_PeersSinglePeer covers peers/* error and empty-state cases using one peer.
func TestCLI_PeersSinglePeer(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(true)

	t.Run("Status_Empty", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "status")
		require.NoError(t, err)
		require.NotEmpty(t, out)
	})

	t.Run("Requests_Empty", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "requests")
		require.NoError(t, err)
		require.Equal(t, "you have no incoming requests\n", out)
	})

	t.Run("Status_InvalidFormat", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "status", "--format", "z")
		require.Error(t, err)
		require.ErrorContains(t, err, "unknown format")
	})

	t.Run("Remove_RequiresPidOrName", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "remove")
		require.Error(t, err)
		require.ErrorContains(t, err, "peerID or name should be defined")
	})
}

// TestCLI_PeersStatus covers "peers status" with actual peers and the --format flag.
func TestCLI_PeersStatus(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.makeFriends(peer1, peer2)

	t.Run("WithPeers", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "status")
		require.NoError(t, err)
		knownPeers, err := peer1.api.KnownPeers()
		require.NoError(t, err)
		require.Len(t, knownPeers, 1)
		// IP appears in the "peer" column; tablewriter uppercases column headers
		require.Contains(t, out, knownPeers[0].IpAddr)
		require.Contains(t, out, "PEER")
		require.Contains(t, out, "STATUS")
		require.Contains(t, out, "LAST SEEN")
		require.Contains(t, out, "VERSION")
	})

	t.Run("Format_ID_Only", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "status", "--format", "i")
		require.NoError(t, err)
		require.Contains(t, out, "PEER ID")
		require.Contains(t, out, peer2.PeerID())
		require.NotContains(t, out, "STATUS")
		require.NotContains(t, out, "LAST SEEN")
		require.NotContains(t, out, "VERSION")
	})
}

// TestCLI_PeersRequests_WithRequest verifies the exact output for a pending friend request.
func TestCLI_PeersRequests_WithRequest(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	err := peer2.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "peer_1"})
	ts.NoError(err)

	ts.Eventually(func() bool {
		out, err := runCLI(ts, peer1, "peers", "requests")
		return err == nil && strings.Contains(out, peer2.PeerID())
	}, 15*time.Second, 100*time.Millisecond)

	out, err := runCLI(ts, peer1, "peers", "requests")
	require.NoError(t, err)

	reqs, err := peer1.api.AuthRequests()
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	// Exact format from printFriendRequests: "Name: '%s' peerID: %s suggestedIP: %s\n"
	expected := fmt.Sprintf("Name: '%s' peerID: %s suggestedIP: %s\n",
		reqs[0].Name, reqs[0].PeerID, reqs[0].SuggestedIP)
	require.Equal(t, expected, out)
}

// TestCLI_PeersAdd covers the two branches of "peers add": sending a new request,
// and accepting an existing one. Each subtest creates independent peers.
// --allow-exit-node has to work in both branches — they call different API
// endpoints — and the bare form (no "=true") has to set it.
func TestCLI_PeersAdd(t *testing.T) {
	t.Run("SendRequest", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer2)

		out, err := runCLI(ts, peer1, "peers", "add", "--pid", peer2.PeerID(), "--name", "peer_2")
		require.NoError(t, err)
		require.Equal(t, "friend request sent successfully\n", out)

		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.False(t, pcfg.WeAllowUsingAsExitNode)
	})

	t.Run("SendRequestAllowingExitNode", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer2)

		out, err := runCLI(ts, peer1, "peers", "add", "--pid", peer2.PeerID(), "--name", "peer_2", "--allow-exit-node")
		require.NoError(t, err)
		require.Equal(t, "friend request sent successfully\n", out)

		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.True(t, pcfg.WeAllowUsingAsExitNode)
	})

	t.Run("AcceptRequest", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer2)

		// peer2 sends to peer1 first; CLI add from peer1 should detect and accept it
		err := peer2.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "peer_1"})
		ts.NoError(err)

		ts.Eventually(func() bool {
			reqs, err := peer1.api.AuthRequests()
			return err == nil && len(reqs) == 1
		}, 15*time.Second, 50*time.Millisecond)

		out, err := runCLI(ts, peer1, "peers", "add", "--pid", peer2.PeerID(), "--name", "peer_2")
		require.NoError(t, err)
		require.Equal(t, "successfully accepted existing invitation from the device 'peer_2'\n", out)

		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.False(t, pcfg.WeAllowUsingAsExitNode)
	})

	t.Run("AcceptRequestAllowingExitNode", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer2)

		err := peer2.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "peer_1"})
		ts.NoError(err)

		ts.Eventually(func() bool {
			reqs, err := peer1.api.AuthRequests()
			return err == nil && len(reqs) == 1
		}, 15*time.Second, 50*time.Millisecond)

		out, err := runCLI(ts, peer1, "peers", "add", "--pid", peer2.PeerID(), "--name", "peer_2", "--allow-exit-node")
		require.NoError(t, err)
		require.Equal(t, "successfully accepted existing invitation from the device 'peer_2'\n", out)

		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.True(t, pcfg.WeAllowUsingAsExitNode)

		// The permission reaches peer2 through the regular status exchange.
		ts.Eventually(func() bool {
			peer1OnPeer2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
			return err == nil && peer1OnPeer2.AllowedUsingAsExitNode
		}, 15*time.Second, 100*time.Millisecond)
	})
}

// TestCLI_PeersInvite covers the invite link lifecycle through the CLI alone,
// on a single peer: creating links, listing them, revoking one. Redeeming a
// link takes two nodes and lives in TestCLI_AddPeerViaInviteLink.
func TestCLI_PeersInvite(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	ts.NoError(peer1.api.UpdateMySettings("alice"))

	t.Run("Empty", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "list")
		require.NoError(t, err)
		require.Equal(t, "you have no invite links\n", out)
	})

	var createdID string

	t.Run("Create", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "create",
			"--alias", "laptop", "--label", "my laptop", "--allow-exit-node")
		require.NoError(t, err)

		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		require.Greater(t, len(lines), 2, "the link, its summary, and a QR code block")

		// The link comes first and alone on its line, so it can be picked out of
		// the output by eye or by a script.
		link, err := entity.ParseInviteLink(lines[0])
		require.NoError(t, err)
		require.Equal(t, peer1.PeerID(), link.PeerID)
		require.Equal(t, "alice", link.Name)
		require.NotEmpty(t, link.Token, "a created invite is a capability, unlike the link from `me id`")

		summary := lines[1]
		require.Contains(t, summary, "uses 0/1")
		require.Contains(t, summary, `alias "laptop"`)
		require.Contains(t, summary, "exit node")
		require.NotContains(t, summary, link.Token)

		invites, err := peer1.api.Invites()
		require.NoError(t, err)
		require.Len(t, invites, 1)
		createdID = invites[0].ID
		require.Contains(t, summary, "id "+createdID)
	})

	t.Run("CreateWithoutQR", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "create", "--no-qr", "--expires", "never")
		require.NoError(t, err)

		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		require.Len(t, lines, 2, "the link and its summary, nothing else")
		require.Contains(t, lines[1], "expires never")

		_, err = entity.ParseInviteLink(lines[0])
		require.NoError(t, err)
	})

	// Days are ours: time.ParseDuration knows no unit above an hour, and a link
	// that lives a week is an ordinary thing to ask for.
	t.Run("CreateExpiringInDays", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "create", "--no-qr", "--expires", "7d", "--uses", "5")
		require.NoError(t, err)
		require.Contains(t, out, "uses 0/5")

		invites, err := peer1.api.Invites()
		require.NoError(t, err)
		require.WithinDuration(t, time.Now().Add(7*24*time.Hour), invites[0].ExpiresAt, time.Minute)
	})

	t.Run("List", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "list")
		require.NoError(t, err)

		require.Contains(t, out, createdID)
		require.Contains(t, out, "my laptop")
		require.Contains(t, out, "0/1")
		require.Contains(t, out, "active")
		require.Contains(t, out, "never")

		invites, err := peer1.api.Invites()
		require.NoError(t, err)
		for _, invite := range invites {
			link, err := entity.ParseInviteLink(invite.Link)
			require.NoError(t, err)
			require.NotContains(t, out, link.Token, "the list must not hand out secrets by default")
		}
	})

	t.Run("ListWithLinks", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "list", "--show-links")
		require.NoError(t, err)

		invites, err := peer1.api.Invites()
		require.NoError(t, err)
		for _, invite := range invites {
			require.Contains(t, out, invite.Link)
		}
	})

	t.Run("Revoke", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "invite", "revoke", "--id", createdID)
		require.NoError(t, err)
		require.Contains(t, out, "peers already added through it stay")

		invites, err := peer1.api.Invites()
		require.NoError(t, err)
		for _, invite := range invites {
			if invite.ID == createdID {
				require.True(t, invite.Revoked)
				require.Equal(t, entity.InviteStatusRevoked, invite.Status)
			}
		}

		out, err = runCLI(ts, peer1, "peers", "invite", "list")
		require.NoError(t, err)
		require.Contains(t, out, "revoked", "a revoked invite stays in the list as history")
	})

	t.Run("RevokeUnknown", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "invite", "revoke", "--id", "0000")
		require.ErrorContains(t, err, "invite not found")
	})

	t.Run("AliasWithMultiUse", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "invite", "create", "--uses", "5", "--alias", "laptop")
		require.ErrorContains(t, err, "single-use")
	})

	// Rejected before any request is made, so the message has to say what a
	// valid value looks like.
	t.Run("BadExpires", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "invite", "create", "--expires", "tomorrow")
		require.ErrorContains(t, err, "invalid expires value")
		require.ErrorContains(t, err, "never")
	})
}

// TestCLI_AddPeerViaInviteLink is the whole point of the CLI half of the
// feature: creating a link on one node and joining by it on another, without
// touching anything but the command line — and without an accept step.
func TestCLI_AddPeerViaInviteLink(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.NoError(peer1.api.UpdateMySettings("alice"))

	out, err := runCLI(ts, peer1, "peers", "invite", "create", "--no-qr",
		"--alias", "laptop", "--allow-exit-node")
	require.NoError(t, err)
	inviteLink, _, _ := strings.Cut(out, "\n")

	// No --name: the link carries one, and that is the point of it.
	out, err = runCLI(ts, peer2, "peers", "add", "--link", inviteLink)
	require.NoError(t, err)
	require.Contains(t, out, "accepts it automatically")

	ts.Eventually(func() bool {
		pcfg, err := peer2.api.KnownPeerConfig(peer1.PeerID())
		return err == nil && pcfg.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	require.Len(t, peer1.app.AuthStatus.GetIngoingAuthRequests(), 0, "nobody had to accept anything")

	peer2OnPeer1, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	require.NoError(t, err)
	require.Equal(t, "laptop", peer2OnPeer1.Alias, "the alias comes from the invite")
	require.True(t, peer2OnPeer1.WeAllowUsingAsExitNode)

	peer1OnPeer2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	require.NoError(t, err)
	require.Equal(t, "alice", peer1OnPeer2.Alias, "the name in the link is what we call the creator")

	out, err = runCLI(ts, peer1, "peers", "invite", "list")
	require.NoError(t, err)
	require.Contains(t, out, "1/1")
	require.Contains(t, out, entity.InviteStatusUsedUp)
}

// TestCLI_PeersAddLinkErrors: the flags of `peers add` stopped being required
// when --link arrived, so the combinations it accepts are now checked by hand.
func TestCLI_PeersAddLinkErrors(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	link := entity.BuildInviteLink(peer2.PeerID(), "", "")

	t.Run("LinkAndPeerID", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "add", "--link", link, "--pid", peer2.PeerID(), "--name", "peer_2")
		require.ErrorContains(t, err, "mutually exclusive")
	})

	t.Run("Neither", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "add", "--name", "peer_2")
		require.ErrorContains(t, err, "either link or pid flag is required")
	})

	t.Run("NoName", func(t *testing.T) {
		// A link that carries no name — the token-less form printed by `me id`
		// for a peer that has not named itself.
		_, err := runCLI(ts, peer1, "peers", "add", "--link", link)
		require.ErrorContains(t, err, "name flag is required")
	})

	t.Run("BrokenLink", func(t *testing.T) {
		_, err := runCLI(ts, peer1, "peers", "add", "--link", "awl://invite?p=nonsense", "--name", "peer_2")
		require.ErrorContains(t, err, "invalid peer id")
	})
}

// TestCLI_PeersAddByTokenlessLink: a link without a token is not an invitation,
// it is the shareable form of a peer id — the add stays as manual as ever.
func TestCLI_PeersAddByTokenlessLink(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	link := entity.BuildInviteLink(peer2.PeerID(), "", "peer_2")

	out, err := runCLI(ts, peer1, "peers", "add", "--link", link)
	require.NoError(t, err)
	require.Equal(t, "friend request sent successfully\n", out, "no token, no promise of an automatic accept")

	pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	require.NoError(t, err)
	require.Equal(t, "peer_2", pcfg.Alias)

	ts.Eventually(func() bool {
		reqs, err := peer2.api.AuthRequests()
		return err == nil && len(reqs) == 1
	}, 15*time.Second, 50*time.Millisecond)
}

// TestCLI_PeersRename covers rename by peer ID and by alias on a shared peer pair.
// ByPID runs first and renames the alias to "renamed_peer"; ByName reuses that alias.
func TestCLI_PeersRename(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.makeFriends(peer1, peer2)

	t.Run("ByPID", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "rename", "--pid", peer2.PeerID(), "--new_name", "renamed_peer")
		require.NoError(t, err)
		require.Equal(t, "peer name updated successfully\n", out)
		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.Equal(t, "renamed_peer", pcfg.Alias)
	})

	// ByName depends on ByPID having set the alias to "renamed_peer"
	t.Run("ByName", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "rename", "--name", "renamed_peer", "--new_name", "cli_renamed")
		require.NoError(t, err)
		require.Equal(t, "peer name updated successfully\n", out)
		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.Equal(t, "cli_renamed", pcfg.Alias)
	})
}

// TestCLI_PeersUpdate covers update_domain, update_ip, and allow_exit_node on a shared pair.
// Each subtest mutates an independent field of peer2's config.
func TestCLI_PeersUpdate(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.makeFriends(peer1, peer2)

	t.Run("Domain", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "update_domain", "--pid", peer2.PeerID(), "--domain", "newdomain")
		require.NoError(t, err)
		require.Equal(t, "peer domain name updated successfully\n", out)
		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.Equal(t, "newdomain", pcfg.DomainName)
	})

	t.Run("IP", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "update_ip", "--pid", peer2.PeerID(), "--ip", "10.66.0.50")
		require.NoError(t, err)
		require.Equal(t, "peer IP address updated successfully\n", out)
		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.Equal(t, "10.66.0.50", pcfg.IPAddr)
	})

	t.Run("AllowExitNode", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "peers", "allow_exit_node", "--pid", peer2.PeerID(), "--allow")
		require.NoError(t, err)
		require.Equal(t, "AllowUsingAsExitNode config updated successfully\n", out)
		pcfg, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		require.NoError(t, err)
		require.True(t, pcfg.WeAllowUsingAsExitNode)
	})
}

// TestCLI_PeersRemove covers remove by peer ID and by alias.
// Each subtest creates its own peers because removal is destructive.
func TestCLI_PeersRemove(t *testing.T) {
	t.Run("ByID", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.makeFriends(peer1, peer2)

		out, err := runCLI(ts, peer1, "peers", "remove", "--pid", peer2.PeerID())
		require.NoError(t, err)
		require.Equal(t, "peer removed successfully\n", out)
		_, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
		require.False(t, exists)
	})

	t.Run("ByName", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.makeFriends(peer1, peer2)
		// makeFriends sets peer2's alias on peer1's side to "peer_2" (sendAndAcceptFriendRequest)

		out, err := runCLI(ts, peer1, "peers", "remove", "--name", "peer_2")
		require.NoError(t, err)
		require.Equal(t, "peer removed successfully\n", out)
		_, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
		require.False(t, exists)
	})
}

// TestCLI_Proxy covers list_proxies and set_proxy with a shared two-friends setup
// where peer1 allows peer2 to use it as an exit node.
func TestCLI_Proxy(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.makeFriends(peer1, peer2)

	// Allow peer2 to use peer1 as exit node (WeAllowUsingAsExitNode on peer1's record of peer2)
	peer2ConfigOnPeer1, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	ts.NoError(err)
	ts.NoError(peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer2.PeerID(),
		Alias:                peer2ConfigOnPeer1.Alias,
		DomainName:           peer2ConfigOnPeer1.DomainName,
		IPAddr:               peer2ConfigOnPeer1.IPAddr,
		AllowUsingAsExitNode: true,
	}))

	ts.Eventually(func() bool {
		proxies, err := peer2.api.ListAvailableProxies()
		return err == nil && len(proxies) > 0
	}, 15*time.Second, 100*time.Millisecond)

	t.Run("ListProxies", func(t *testing.T) {
		out, err := runCLI(ts, peer2, "me", "list_proxies")
		require.NoError(t, err)
		proxies, err := peer2.api.ListAvailableProxies()
		require.NoError(t, err)
		require.Len(t, proxies, 1)
		// Exact format from listProxies: "Proxies:\n- peer name: %s | peer id: %s\n"
		expected := fmt.Sprintf("Proxies:\n- peer name: %s | peer id: %s\n",
			proxies[0].PeerName, proxies[0].PeerID)
		require.Equal(t, expected, out)
	})

	t.Run("SetProxy", func(t *testing.T) {
		// Set proxy to peer1
		out, err := runCLI(ts, peer2, "me", "set_proxy", "--pid", peer1.PeerID())
		require.NoError(t, err)
		require.Equal(t, "proxy settings updated successfully\n", out)

		info, err := peer2.api.PeerInfo()
		require.NoError(t, err)
		require.Equal(t, peer1.PeerID(), info.SOCKS5.UsingPeerID)

		// Clear proxy (no --pid means empty string → disable)
		out, err = runCLI(ts, peer2, "me", "set_proxy")
		require.NoError(t, err)
		require.Equal(t, "proxy settings updated successfully\n", out)

		info, err = peer2.api.PeerInfo()
		require.NoError(t, err)
		require.Equal(t, "", info.SOCKS5.UsingPeerID)
	})
}

// TestCLI_Gateway covers all `awl peers gateway` subcommands. setupGatewayPeers
// leaves the client with gateway enabled (Tunnel-side) and the exit node with
// ServeAsVPNGateway=true, so this exercises every endpoint against a realistic
// state. Subtests are ordered so DisableGateway runs before re-enabling, and
// the exit_node toggle runs on the exitNode peer where it makes sense.
func TestCLI_Gateway(t *testing.T) {
	skipIfVPNGatewayUnsupported(t)
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	t.Run("StatusEnabled", func(t *testing.T) {
		out, err := runCLI(ts, client, "gateway", "status")
		require.NoError(t, err)
		require.Regexp(t, `Client:\s+enabled`, out)
		require.Regexp(t, `Server:\s+disabled`, out)
		require.Contains(t, out, "Gateway peer:")
		require.Contains(t, out, exitNode.PeerID())
		require.Regexp(t, `Connection:\s+(direct|via relay)`, out)
	})

	t.Run("List", func(t *testing.T) {
		out, err := runCLI(ts, client, "gateway", "list")
		require.NoError(t, err)
		require.Contains(t, out, "Available VPN gateways:")
		require.Contains(t, out, exitNode.PeerID())
		require.Contains(t, out, "[connected]")
	})

	t.Run("ClientStop", func(t *testing.T) {
		out, err := runCLI(ts, client, "gateway", "client", "stop")
		require.NoError(t, err)
		require.Equal(t, "VPN gateway client disabled\n", out)

		info, err := client.api.PeerInfo()
		require.NoError(t, err)
		require.False(t, info.VPNGateway.ClientEnabled)
	})

	t.Run("ClientUseByPid", func(t *testing.T) {
		out, err := runCLI(ts, client, "gateway", "client", "use", "--pid", exitNode.PeerID())
		require.NoError(t, err)
		require.Contains(t, out, "VPN gateway client enabled, routing via ")
		require.Contains(t, out, exitNode.PeerID())

		info, err := client.api.PeerInfo()
		require.NoError(t, err)
		require.True(t, info.VPNGateway.ClientEnabled)
		require.Equal(t, exitNode.PeerID(), info.VPNGateway.GatewayPeerID)

		// `me status` must reflect the new state on the same row.
		statusOut, err := runCLI(ts, client, "me", "status")
		require.NoError(t, err)
		require.Contains(t, statusOut, "VPN gateway client")
		require.Contains(t, statusOut, "peer_2")
		require.Contains(t, statusOut, "[connected]")
		// The gateway detail row shows the connection path once connected.
		require.True(t, strings.Contains(statusOut, "direct") || strings.Contains(statusOut, "via relay"),
			"gateway detail row should show connection path")
	})

	t.Run("ServerDisable", func(t *testing.T) {
		out, err := runCLI(ts, exitNode, "gateway", "server", "disable")
		require.NoError(t, err)
		require.Equal(t, "VPN gateway server disabled\n", out)

		info, err := exitNode.api.PeerInfo()
		require.NoError(t, err)
		require.False(t, info.VPNGateway.ServerEnabled)
	})

	t.Run("ServerEnable", func(t *testing.T) {
		out, err := runCLI(ts, exitNode, "gateway", "server", "enable")
		require.NoError(t, err)
		require.Equal(t, "VPN gateway server enabled\n", out)

		info, err := exitNode.api.PeerInfo()
		require.NoError(t, err)
		require.True(t, info.VPNGateway.ServerEnabled)
	})
}

// TestCLI_ConnectionFailure verifies an error is returned when the daemon is unreachable.
func TestCLI_ConnectionFailure(t *testing.T) {
	_, err := runCLIAddr("127.0.0.1:1", "me", "status")
	require.Error(t, err)
}
