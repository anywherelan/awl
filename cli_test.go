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
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/update"
)

// runCLIAddr runs a CLI command against the given API address and returns stdout output.
func runCLIAddr(addr string, args ...string) (string, error) {
	app := cli.New(update.AppTypeAwl)
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
			"Download rate", "Upload rate", "Bootstrap peers",
			"DNS", "SOCKS5 Proxy", "SOCKS5 Proxy address",
			"SOCKS5 Proxy exit node", "Reachability", "Uptime", "Server version",
		} {
			require.Contains(t, out, label)
		}
		require.Contains(t, out, "not working") // DNS disabled in test config
		require.Contains(t, out, "working")     // SOCKS5 enabled in test config
		require.Contains(t, out, "dev")         // config.Version in tests
	})

	t.Run("Id", func(t *testing.T) {
		out, err := runCLI(ts, peer1, "me", "id")
		require.NoError(t, err)
		lines := strings.SplitN(strings.TrimRight(out, "\n"), "\n", 2)
		require.Len(t, lines, 2)
		require.Equal(t, fmt.Sprintf("your peer id: %s", peer1.PeerID()), lines[0])
		require.NotEmpty(t, lines[1]) // QR code block follows
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

	err := peer2.api.SendFriendRequest(peer1.PeerID(), "peer_1", "")
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
func TestCLI_PeersAdd(t *testing.T) {
	t.Run("SendRequest", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer2)

		out, err := runCLI(ts, peer1, "peers", "add", "--pid", peer2.PeerID(), "--name", "peer_2")
		require.NoError(t, err)
		require.Equal(t, "friend request sent successfully\n", out)
	})

	t.Run("AcceptRequest", func(t *testing.T) {
		ts := NewTestSuite(t)
		peer1 := ts.NewTestPeer(false)
		peer2 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer2)

		// peer2 sends to peer1 first; CLI add from peer1 should detect and accept it
		err := peer2.api.SendFriendRequest(peer1.PeerID(), "peer_1", "")
		ts.NoError(err)

		ts.Eventually(func() bool {
			reqs, err := peer1.api.AuthRequests()
			return err == nil && len(reqs) == 1
		}, 15*time.Second, 50*time.Millisecond)

		out, err := runCLI(ts, peer1, "peers", "add", "--pid", peer2.PeerID(), "--name", "peer_2")
		require.NoError(t, err)
		require.Equal(t, "user added to friends list successfully\n", out)
	})
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

// TestCLI_ConnectionFailure verifies an error is returned when the daemon is unreachable.
func TestCLI_ConnectionFailure(t *testing.T) {
	_, err := runCLIAddr("127.0.0.1:1", "me", "status")
	require.Error(t, err)
}
