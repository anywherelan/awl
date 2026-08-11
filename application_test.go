package awl

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/integrationtests/tools/israce"
	"golang.org/x/net/proxy"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/service"
	"github.com/anywherelan/awl/vpn"
)

func TestMakeFriends(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	ts.makeFriends(peer2, peer1)
}

func TestRemovePeer(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	ts.makeFriends(peer2, peer1)

	// Remove peer2 from peer1
	err := peer1.api.RemovePeer(peer2.PeerID())
	ts.NoError(err)

	peer2From1, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	ts.EqualError(err, "status code: 404, error: peer not found")
	ts.Nil(peer2From1)
	_, blockedPeerExists := peer1.app.Conf.GetBlockedPeer(peer2.PeerID())
	ts.True(blockedPeerExists)

	time.Sleep(500 * time.Millisecond)
	peer1From2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)
	ts.NotNil(peer1From2)
	ts.True(peer1From2.Confirmed)
	ts.True(peer1From2.Declined)

	ts.Len(peer1.app.AuthStatus.GetIngoingAuthRequests(), 0)
	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)

	// Add peer2 from peer1 - should succeed
	err = peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer2.PeerID(), Alias: "peer_2"})
	ts.NoError(err)
	time.Sleep(500 * time.Millisecond)

	peer2From1, err = peer1.api.KnownPeerConfig(peer2.PeerID())
	ts.NoError(err)
	ts.True(peer2From1.Confirmed)
	ts.False(peer2From1.Declined)

	_, blockedPeerExists = peer1.app.Conf.GetBlockedPeer(peer2.PeerID())
	ts.False(blockedPeerExists)

	peer1From2, err = peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)
	ts.NotNil(peer1From2)
	ts.True(peer1From2.Confirmed)
	ts.False(peer1From2.Declined)

	ts.Len(peer1.app.AuthStatus.GetIngoingAuthRequests(), 0)
	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)

	// test ping. Latency is recorded asynchronously by the status/auth exchange
	// the re-add above kicks off in a goroutine (RecordPeerLatency); on slow CI
	// the fixed sleeps aren't always enough for it to land, so poll for it rather
	// than asserting once.
	ts.Eventually(func() bool {
		return peer1.app.P2p.GetPeerLatency(peer2.app.P2p.PeerID()) != 0
	}, 15*time.Second, 100*time.Millisecond, "peer latency should be recorded after the status exchange")
}

func TestDeclinePeerFriendRequest(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer2.PeerID(), Alias: "peer_2"})
	ts.NoError(err)

	var authRequests []entity.AuthRequest
	ts.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		ts.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.ReplyFriendRequest(entity.FriendRequestReply{PeerID: authRequests[0].PeerID, Alias: "peer_1", Decline: true})
	ts.NoError(err)

	time.Sleep(500 * time.Millisecond)
	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.False(knownPeer.Confirmed)
	ts.True(knownPeer.Declined)

	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
	_, blockedPeerExists := peer2.app.Conf.GetBlockedPeer(peer1.PeerID())
	ts.True(blockedPeerExists)

	blocked, err := peer2.api.BlockedPeers()
	ts.NoError(err)
	ts.Len(blocked, 1)
	ts.Equal(peer1.PeerID(), blocked[0].PeerID)
}

func TestAutoAcceptFriendRequest(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	peer2.app.Conf.Lock()
	peer2.app.Conf.P2pNode.AutoAcceptAuthRequests = true
	peer2.app.Conf.Unlock()

	err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer2.PeerID(), Alias: "peer_2"})
	ts.NoError(err)

	ts.Eventually(func() bool {
		knownPeers, err := peer2.api.KnownPeers()
		ts.NoError(err)
		return len(knownPeers) == 1
	}, 15*time.Second, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.True(knownPeer.Confirmed)
	ts.False(knownPeer.Declined)

	knownPeer, exists = peer2.app.Conf.GetPeer(peer1.PeerID())
	ts.True(exists)
	ts.True(knownPeer.Confirmed)
	ts.False(knownPeer.Declined)
}

func TestFriendRequestWithCustomIP(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.ensurePeersAvailableInDHT(peer1, peer3)

	t.Run("InviteWithCustomIP", func(t *testing.T) {
		customIP := "10.66.0.222"
		err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer2.PeerID(), Alias: "peer_2", IPAddr: customIP})
		ts.NoError(err)

		// Check immediate state on peer1
		p2, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
		ts.True(exists)
		ts.Equal(customIP, p2.IPAddr)
	})

	t.Run("RespondWithCustomIP", func(t *testing.T) {
		err := peer3.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "peer_1"})
		ts.NoError(err)

		var authRequests []entity.AuthRequest
		ts.Eventually(func() bool {
			authRequests, err = peer1.api.AuthRequests()
			ts.NoError(err)
			return len(authRequests) == 1
		}, 15*time.Second, 50*time.Millisecond)

		customIP := "10.66.0.223"
		err = peer1.api.ReplyFriendRequest(entity.FriendRequestReply{PeerID: authRequests[0].PeerID, Alias: "peer_3", IPAddr: customIP})
		ts.NoError(err)

		p3, exists := peer1.app.Conf.GetPeer(peer3.PeerID())
		ts.True(exists)
		ts.Equal(customIP, p3.IPAddr)
	})

	t.Run("InviteWithInvalidIP", func(t *testing.T) {
		peer4 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer4)

		err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer4.PeerID(), Alias: "peer_4", IPAddr: "invalid-ip"})
		ts.Error(err)
		ts.ErrorContains(err, "Field validation for 'IPAddr' failed")
	})

	t.Run("InviteWithDuplicateIP", func(t *testing.T) {
		peer5 := ts.NewTestPeer(false)
		ts.ensurePeersAvailableInDHT(peer1, peer5)

		// Try to use peer2's IP which is 10.66.0.222
		err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer5.PeerID(), Alias: "peer_5", IPAddr: "10.66.0.222"})
		ts.Error(err)
		ts.ErrorContains(err, "ip 10.66.0.222 is already used by peer")
	})
}

func TestGetAuthRequestsSuggestedIP(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.ensurePeersAvailableInDHT(peer1, peer3)

	err := peer2.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "peer_1"})
	ts.NoError(err)
	time.Sleep(200 * time.Millisecond)

	err = peer3.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "peer_1"})
	ts.NoError(err)

	ts.Eventually(func() bool {
		reqs, err := peer1.api.AuthRequests()
		ts.NoError(err)
		return len(reqs) == 2
	}, 15*time.Second, 50*time.Millisecond)

	reqs, err := peer1.api.AuthRequests()
	ts.NoError(err)
	ts.Len(reqs, 2)

	// Verify they are valid and unused
	ips := []string{reqs[0].SuggestedIP, reqs[1].SuggestedIP}
	sort.Strings(ips)
	ts.Equal("10.66.0.2", ips[0])
	ts.Equal("10.66.0.3", ips[1])

	err = peer1.app.Conf.CheckIPUnique(ips[0], "")
	ts.NoError(err)
	err = peer1.app.Conf.CheckIPUnique(ips[1], "")
	ts.NoError(err)
}

func TestUniquePeerAlias(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.ensurePeersAvailableInDHT(peer2, peer3)

	err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer2.PeerID(), Alias: "peer"})
	ts.NoError(err)

	time.Sleep(200 * time.Millisecond)

	err = peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer3.PeerID(), Alias: "peer"})
	ts.EqualError(err, "status code: 400, error: "+api.ErrorPeerAliasIsNotUniq)
}

func TestUpdateUseAsExitNodeConfig(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	ts.makeFriends(peer2, peer1)

	info, err := peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal("", info.SOCKS5.UsingPeerID)

	availableProxies, err := peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 0)

	peer1Config, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)
	ts.Equal(false, peer1Config.AllowedUsingAsExitNode)

	// allow, check that peer1 got our config
	err = peer2.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer1.PeerID(),
		Alias:                peer1Config.Alias,
		DomainName:           peer1Config.DomainName,
		IPAddr:               peer1Config.IPAddr,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	var peer2Config *config.KnownPeer
	ts.Eventually(func() bool {
		peer2Config, err = peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)

		return peer2Config.AllowedUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	// peer2 allowing us does NOT select it as our exit node — the choice is always explicit
	info, err = peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal("", info.SOCKS5.UsingPeerID)

	availableProxies, err = peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 1)

	err = peer1.api.UpdateProxySettings(peer2.PeerID())
	ts.NoError(err)

	info, err = peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal(peer2.PeerID(), info.SOCKS5.UsingPeerID)

	// allow from peer1, check that peer2 got our config
	err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer2.PeerID(),
		Alias:                peer2Config.Alias,
		DomainName:           peer2Config.DomainName,
		IPAddr:               peer2Config.IPAddr,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		peer1Config, err := peer2.api.KnownPeerConfig(peer1.PeerID())
		ts.NoError(err)

		return peer1Config.AllowedUsingAsExitNode && peer1Config.WeAllowUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	ts.Equal("", peer2.app.SOCKS5.GetProxyPeerID())

	// peer2 has to pick peer1 explicitly too; the checks below proxy through it.
	err = peer2.api.UpdateProxySettings(peer1.PeerID())
	ts.NoError(err)
	ts.Equal(peer1.PeerID(), peer2.app.SOCKS5.GetProxyPeerID())

	// disallow from peer2, check that peer1 got our new config
	err = peer2.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer1.PeerID(),
		Alias:                peer1Config.Alias,
		DomainName:           peer1Config.DomainName,
		IPAddr:               peer1Config.IPAddr,
		AllowUsingAsExitNode: false,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)

		return !peer2Config.AllowedUsingAsExitNode && peer2Config.WeAllowUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	peer1.app.Conf.Lock()
	peer1Socks5UsingPeerID := peer1.app.Conf.SOCKS5.UsingPeerID
	peer1.app.Conf.Unlock()
	ts.Equal("", peer1Socks5UsingPeerID)

	availableProxies, err = peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 0)

	testSOCKS5Proxy(ts, peer1.app.Conf.SOCKS5.ListenAddress, fmt.Sprintf("%s %s", "unknown error", "general SOCKS server failure"))

	testSOCKS5Proxy(ts, peer2.app.Conf.SOCKS5.ListenAddress, fmt.Sprintf("%s %s", "unknown error", "connection not allowed by ruleset"))

	peer1.app.SOCKS5.SetProxyingLocalhostEnabled(true)
	testSOCKS5Proxy(ts, peer2.app.Conf.SOCKS5.ListenAddress, "")
	peer1.app.SOCKS5.SetProxyingLocalhostEnabled(false)

	// Testing API
	err = peer1.api.UpdateProxySettings(peer2.PeerID())
	ts.ErrorContains(err, "peer doesn't allow using as exit node")

	err = peer2.api.UpdateProxySettings("asd")
	ts.ErrorContains(err, "peer not found")

	info, err = peer2.api.PeerInfo()
	ts.NoError(err)
	ts.Equal(peer1.PeerID(), info.SOCKS5.UsingPeerID)

	err = peer2.api.UpdateProxySettings("")
	ts.NoError(err)

	info, err = peer2.api.PeerInfo()
	ts.NoError(err)
	ts.Equal("", info.SOCKS5.UsingPeerID)
}

// TestAddPeerWithExitNodePermission covers granting AllowUsingAsExitNode at add
// time — in the outgoing invite and in the reply to an incoming one — instead of
// a follow-up update_settings call. The permission has to reach the other side
// through the regular status exchange.
func TestAddPeerWithExitNodePermission(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.ensurePeersAvailableInDHT(peer1, peer3)

	t.Run("InviteWithExitNode", func(t *testing.T) {
		err := peer1.api.SendFriendRequest(entity.FriendRequest{
			PeerID:               peer2.PeerID(),
			Alias:                "peer_2",
			AllowUsingAsExitNode: true,
		})
		ts.NoError(err)

		peer2OnPeer1, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		ts.True(peer2OnPeer1.WeAllowUsingAsExitNode)

		var authRequests []entity.AuthRequest
		ts.Eventually(func() bool {
			authRequests, err = peer2.api.AuthRequests()
			ts.NoError(err)
			return len(authRequests) == 1
		}, 15*time.Second, 50*time.Millisecond)
		err = peer2.api.ReplyFriendRequest(entity.FriendRequestReply{
			PeerID: authRequests[0].PeerID,
			Alias:  "peer_1",
		})
		ts.NoError(err)

		// peer2 learns the permission from the status exchange, without anyone
		// calling update_settings.
		ts.Eventually(func() bool {
			peer1OnPeer2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
			ts.NoError(err)
			return peer1OnPeer2.AllowedUsingAsExitNode
		}, 15*time.Second, 100*time.Millisecond)

		peer1OnPeer2, err := peer2.api.KnownPeerConfig(peer1.PeerID())
		ts.NoError(err)
		ts.False(peer1OnPeer2.WeAllowUsingAsExitNode, "the permission is one-way")

		// Being allowed is not the same as being selected, see clearSelectedExitNode.
		availableProxies, err := peer2.api.ListAvailableProxies()
		ts.NoError(err)
		ts.Len(availableProxies, 1)
		info, err := peer2.api.PeerInfo()
		ts.NoError(err)
		ts.Equal("", info.SOCKS5.UsingPeerID)
	})

	t.Run("AcceptWithExitNode", func(t *testing.T) {
		err := peer3.api.SendFriendRequest(entity.FriendRequest{
			PeerID: peer1.PeerID(),
			Alias:  "peer_1",
		})
		ts.NoError(err)

		var authRequests []entity.AuthRequest
		ts.Eventually(func() bool {
			authRequests, err = peer1.api.AuthRequests()
			ts.NoError(err)
			return len(authRequests) == 1
		}, 15*time.Second, 50*time.Millisecond)
		err = peer1.api.ReplyFriendRequest(entity.FriendRequestReply{
			PeerID:               authRequests[0].PeerID,
			Alias:                "peer_3",
			AllowUsingAsExitNode: true,
		})
		ts.NoError(err)

		peer3OnPeer1, err := peer1.api.KnownPeerConfig(peer3.PeerID())
		ts.NoError(err)
		ts.True(peer3OnPeer1.WeAllowUsingAsExitNode)

		ts.Eventually(func() bool {
			peer1OnPeer3, err := peer3.api.KnownPeerConfig(peer1.PeerID())
			ts.NoError(err)
			return peer1OnPeer3.AllowedUsingAsExitNode
		}, 15*time.Second, 100*time.Millisecond)
	})
}

// TestAddPeerViaInviteLink is the invite link happy path: the creator makes a
// link, the redeemer adds the creator with the token from it, and both sides end
// up confirmed with the invite's settings applied — accept_peer is never called
// anywhere in this test.
func TestAddPeerViaInviteLink(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)
	ts.ensurePeersAvailableInDHT(creator, peer3)

	err := creator.api.UpdateMySettings("alice")
	ts.NoError(err)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{
		Label:                "my laptop",
		Alias:                "laptop",
		AllowUsingAsExitNode: true,
		ExpiresInSeconds:     int64(time.Hour.Seconds()),
	})
	ts.NoError(err)

	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)
	ts.Equal(creator.PeerID(), link.PeerID)
	ts.Equal("alice", link.Name)

	err = peer2.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID,
		Alias:  link.Name,
		Token:  link.Token,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		knownPeer, exists := peer2.app.Conf.GetPeer(creator.PeerID())
		return exists && knownPeer.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	ts.Len(creator.app.AuthStatus.GetIngoingAuthRequests(), 0, "the request was never queued for a manual accept")

	peer2OnCreator, exists := creator.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.True(peer2OnCreator.Confirmed)
	ts.Equal("laptop", peer2OnCreator.Alias, "the alias comes from the invite")
	ts.True(peer2OnCreator.WeAllowUsingAsExitNode)
	ts.Equal(invite.ID, peer2OnCreator.InviteID)

	creatorOnPeer2, exists := peer2.app.Conf.GetPeer(creator.PeerID())
	ts.True(exists)
	ts.Empty(creatorOnPeer2.PendingInviteToken, "the token is dropped once we are confirmed")
	ts.Empty(creatorOnPeer2.InviteID, "the marker belongs to the side that issued the link")

	// The permission from the invite reaches the peer through the status exchange.
	ts.Eventually(func() bool {
		knownPeer, _ := peer2.app.Conf.GetPeer(creator.PeerID())
		return knownPeer.AllowedUsingAsExitNode
	}, 15*time.Second, 50*time.Millisecond)

	invites, err := creator.api.Invites()
	ts.NoError(err)
	ts.Len(invites, 1)
	ts.Equal(1, invites[0].UsedCount)
	ts.Equal(entity.InviteStatusUsedUp, invites[0].Status)

	knownPeers, err := creator.api.KnownPeers()
	ts.NoError(err)
	ts.Len(knownPeers, 1)
	ts.Equal(invite.ID, knownPeers[0].InviteID, "the peers list shows which invite let this peer in")

	// An invite whose alias is already taken must not lose the peer: AddPeer
	// rejects a duplicate alias outright, and nobody would see that error.
	t.Run("AliasCollision", func(t *testing.T) {
		secondInvite, err := creator.api.CreateInvite(entity.CreateInviteRequest{Alias: "laptop"})
		ts.NoError(err)
		secondLink, err := entity.ParseInviteLink(secondInvite.Link)
		ts.NoError(err)

		err = peer3.api.SendFriendRequest(entity.FriendRequest{
			PeerID: secondLink.PeerID,
			Alias:  "alice",
			Token:  secondLink.Token,
		})
		ts.NoError(err)

		ts.Eventually(func() bool {
			knownPeer, exists := creator.app.Conf.GetPeer(peer3.PeerID())
			return exists && knownPeer.Confirmed
		}, 15*time.Second, 50*time.Millisecond)

		peer3OnCreator, _ := creator.app.Conf.GetPeer(peer3.PeerID())
		ts.Equal("laptop_0", peer3OnCreator.Alias)
		ts.Equal(secondInvite.ID, peer3OnCreator.InviteID)
	})
}

// TestInviteLinkAlreadyKnownPeer covers the case where the redeemer is already
// in the creator's KnownPeers (the creator invited it the usual way earlier).
// An invite only ever lets a new peer in: the peer is already added, with
// settings the creator chose by hand, and a link presented afterwards changes
// nothing and costs the link nothing. The connection comes up regardless — the
// creator answers "you are in my KnownPeers" and the status exchange that
// follows confirms both sides, exactly as in any mutual add.
func TestInviteLinkAlreadyKnownPeer(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)

	// The creator invites peer2 the ordinary way; peer2 never replies to it.
	err := creator.api.SendFriendRequest(entity.FriendRequest{PeerID: peer2.PeerID(), Alias: "peer_2"})
	ts.NoError(err)
	ts.Eventually(func() bool {
		return len(peer2.app.AuthStatus.GetIngoingAuthRequests()) == 1
	}, 15*time.Second, 50*time.Millisecond)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{AllowUsingAsExitNode: true})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	err = peer2.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID,
		Alias:  "creator",
		Token:  link.Token,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		knownPeer, exists := creator.app.Conf.GetPeer(peer2.PeerID())
		return exists && knownPeer.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	peer2OnCreator, _ := creator.app.Conf.GetPeer(peer2.PeerID())
	ts.Equal("peer_2", peer2OnCreator.Alias, "the alias the creator chose earlier stays")
	ts.False(peer2OnCreator.WeAllowUsingAsExitNode, "the link cannot grant anything to a peer already added by hand")
	ts.Empty(peer2OnCreator.InviteID, "the peer did not come in through the link")

	ts.Eventually(func() bool {
		knownPeer, exists := peer2.app.Conf.GetPeer(creator.PeerID())
		return exists && knownPeer.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	ts.Len(creator.app.AuthStatus.GetIngoingAuthRequests(), 0, "an already known peer is not queued for a manual accept")

	invites, err := creator.api.Invites()
	ts.NoError(err)
	ts.Equal(0, invites[0].UsedCount, "nothing was redeemed")
	ts.Equal(entity.InviteStatusActive, invites[0].Status, "the link is still good for somebody else")
}

// TestInviteLinkNotRedeemed checks that a token that no longer works degrades
// into an ordinary auth request instead of failing outright, and that a
// rejected token spends nothing.
func TestInviteLinkNotRedeemed(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)
	ts.ensurePeersAvailableInDHT(creator, peer3)

	requirePendingAuthRequest := func(from TestPeer) {
		var authRequests []entity.AuthRequest
		ts.Eventually(func() bool {
			var err error
			authRequests, err = creator.api.AuthRequests()
			ts.NoError(err)
			for _, req := range authRequests {
				if req.PeerID == from.PeerID() {
					return true
				}
			}
			return false
		}, 15*time.Second, 50*time.Millisecond)

		for _, req := range authRequests {
			ts.Empty(req.Token, "the auth requests endpoint must never hand out a token")
		}
		_, exists := creator.app.Conf.GetPeer(from.PeerID())
		ts.False(exists, "the peer waits for a manual accept")
	}

	t.Run("Revoked", func(t *testing.T) {
		invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{})
		ts.NoError(err)
		link, err := entity.ParseInviteLink(invite.Link)
		ts.NoError(err)
		ts.NoError(creator.api.RevokeInvite(invite.ID))

		err = peer2.api.SendFriendRequest(entity.FriendRequest{
			PeerID: link.PeerID,
			Alias:  "creator",
			Token:  link.Token,
		})
		ts.NoError(err)

		requirePendingAuthRequest(peer2)

		stored, exists := creator.app.Conf.GetInvite(invite.ID)
		ts.True(exists)
		ts.Equal(0, stored.UsedCount)
	})

	t.Run("Expired", func(t *testing.T) {
		invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{ExpiresInSeconds: 60})
		ts.NoError(err)
		link, err := entity.ParseInviteLink(invite.Link)
		ts.NoError(err)

		// Move the expiry into the past instead of waiting for it.
		creator.app.Conf.Lock()
		stored := creator.app.Conf.Invites[invite.ID]
		stored.ExpiresAt = time.Now().Add(-time.Hour)
		creator.app.Conf.Invites[invite.ID] = stored
		creator.app.Conf.Unlock()

		err = peer3.api.SendFriendRequest(entity.FriendRequest{
			PeerID: link.PeerID,
			Alias:  "creator",
			Token:  link.Token,
		})
		ts.NoError(err)

		requirePendingAuthRequest(peer3)

		stored, _ = creator.app.Conf.GetInvite(invite.ID)
		ts.Equal(0, stored.UsedCount)
	})
}

// TestInviteLinkSingleUse: the second holder of a single-use link gets nothing
// but the ordinary manual path.
func TestInviteLinkSingleUse(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)
	ts.ensurePeersAvailableInDHT(creator, peer3)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{MaxUses: 1})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	err = peer2.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID, Alias: "creator", Token: link.Token,
	})
	ts.NoError(err)
	ts.Eventually(func() bool {
		knownPeer, exists := creator.app.Conf.GetPeer(peer2.PeerID())
		return exists && knownPeer.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	err = peer3.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID, Alias: "creator", Token: link.Token,
	})
	ts.NoError(err)
	ts.Eventually(func() bool {
		authRequests, err := creator.api.AuthRequests()
		ts.NoError(err)
		return len(authRequests) == 1 && authRequests[0].PeerID == peer3.PeerID()
	}, 15*time.Second, 50*time.Millisecond)

	_, exists := creator.app.Conf.GetPeer(peer3.PeerID())
	ts.False(exists)

	stored, _ := creator.app.Conf.GetInvite(invite.ID)
	ts.Equal(1, stored.UsedCount, "a spent link cannot be spent again")
}

// TestInviteLinkFromBlockedPeer: blocking outranks an invite. The blocked peer
// is declined as usual, the creator is not even told about it, and the link is
// left untouched.
func TestInviteLinkFromBlockedPeer(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)

	creator.app.Conf.UpsertBlockedPeer(peer2.PeerID(), "unwanted")

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{AllowUsingAsExitNode: true})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	err = peer2.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID,
		Alias:  "creator",
		Token:  link.Token,
	})
	ts.NoError(err)

	// The declined answer is what tells us the creator has processed the request.
	ts.Eventually(func() bool {
		knownPeer, exists := peer2.app.Conf.GetPeer(creator.PeerID())
		return exists && knownPeer.Declined
	}, 15*time.Second, 50*time.Millisecond)

	_, exists := creator.app.Conf.GetPeer(peer2.PeerID())
	ts.False(exists)
	ts.Len(creator.app.AuthStatus.GetIngoingAuthRequests(), 0, "a blocked peer is not offered for a manual accept either")

	stored, _ := creator.app.Conf.GetInvite(invite.ID)
	ts.Equal(0, stored.UsedCount, "a blocked peer must not spend a use")

	// A declined peer stops retrying, so its token is not presented again.
	_, outgoing := peer2.app.AuthStatus.GetAuthRequestCounts()
	ts.Equal(0, outgoing)
}

// TestRemovePeerStopsInviteTokenRetries: removing a peer we added by link must
// also stop the auth retries carrying the invite token. Otherwise the token
// keeps being handed to a peer the user got rid of, and once that peer comes
// online it auto-accepts a request nobody wanted any more.
func TestRemovePeerStopsInviteTokenRetries(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	// Revoked, so the creator queues a manual request instead of confirming and
	// the redeemer keeps retrying with the token.
	ts.NoError(creator.api.RevokeInvite(invite.ID))

	err = peer2.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID,
		Alias:  "creator",
		Token:  link.Token,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		_, outgoing := peer2.app.AuthStatus.GetAuthRequestCounts()
		return outgoing == 1
	}, 15*time.Second, 50*time.Millisecond)

	err = peer2.api.RemovePeer(creator.PeerID())
	ts.NoError(err)

	_, outgoing := peer2.app.AuthStatus.GetAuthRequestCounts()
	ts.Equal(0, outgoing, "no retry may carry the token to a removed peer")
}

// TestInviteLinkWithAutoAccept: a valid token applies the invite's settings even
// when AutoAcceptAuthRequests would have let the peer in anyway. Without this,
// turning on the global auto-accept would silently drop the invite's settings
// and leave the link unspent.
func TestInviteLinkWithAutoAccept(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)

	creator.app.Conf.Lock()
	creator.app.Conf.P2pNode.AutoAcceptAuthRequests = true
	creator.app.Conf.Unlock()

	// Plain auto-accept names a peer after itself, so an alias of "bob" would
	// mean the invite was ignored.
	err := peer2.api.UpdateMySettings("bob")
	ts.NoError(err)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{
		Alias:                "laptop",
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	err = peer2.api.SendFriendRequest(entity.FriendRequest{
		PeerID: link.PeerID,
		Alias:  "creator",
		Token:  link.Token,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		knownPeer, exists := creator.app.Conf.GetPeer(peer2.PeerID())
		return exists && knownPeer.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	peer2OnCreator, _ := creator.app.Conf.GetPeer(peer2.PeerID())
	ts.Equal("laptop", peer2OnCreator.Alias, "the invite's alias wins over the auto-accept default")
	ts.True(peer2OnCreator.WeAllowUsingAsExitNode)
	ts.Equal(invite.ID, peer2OnCreator.InviteID)

	stored, _ := creator.app.Conf.GetInvite(invite.ID)
	ts.Equal(1, stored.UsedCount, "the link is spent, not bypassed by auto-accept")
}

// TestInviteLinkUseAccounting covers who spends a use of a link and who does not.
func TestInviteLinkUseAccounting(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(creator, peer2)
	ts.ensurePeersAvailableInDHT(creator, peer3)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{MaxUses: 2})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	// One link rolling out several devices: both are added, and only then is it
	// used up.
	t.Run("MultiUse", func(t *testing.T) {
		for _, redeemer := range []TestPeer{peer2, peer3} {
			err = redeemer.api.SendFriendRequest(entity.FriendRequest{
				PeerID: link.PeerID,
				Alias:  "creator",
				Token:  link.Token,
			})
			ts.NoError(err)

			ts.Eventually(func() bool {
				knownPeer, exists := creator.app.Conf.GetPeer(redeemer.PeerID())
				return exists && knownPeer.Confirmed
			}, 15*time.Second, 50*time.Millisecond)

			knownPeer, _ := creator.app.Conf.GetPeer(redeemer.PeerID())
			ts.Equal(invite.ID, knownPeer.InviteID)
		}

		invites, err := creator.api.Invites()
		ts.NoError(err)
		ts.Len(invites, 1)
		ts.Equal(2, invites[0].UsedCount)
		ts.Equal(entity.InviteStatusUsedUp, invites[0].Status)
	})

	// A peer already in KnownPeers has nothing to redeem: a retry carrying a
	// token must not eat a use of an unrelated link.
	t.Run("KnownPeerKeepsUse", func(t *testing.T) {
		freshInvite, err := creator.api.CreateInvite(entity.CreateInviteRequest{})
		ts.NoError(err)
		freshLink, err := entity.ParseInviteLink(freshInvite.Link)
		ts.NoError(err)

		err = peer2.app.AuthStatus.SendAuthRequest(context.Background(), creator.app.P2p.PeerID(), protocol.AuthPeer{
			Name:  "peer_2",
			Token: freshLink.Token,
		})
		ts.NoError(err)

		stored, _ := creator.app.Conf.GetInvite(freshInvite.ID)
		ts.Equal(0, stored.UsedCount)

		peer2OnCreator, _ := creator.app.Conf.GetPeer(peer2.PeerID())
		ts.Equal(invite.ID, peer2OnCreator.InviteID, "the marker keeps pointing at the link the peer actually came from")
	})
}

// TestInviteTokenSurvivesRestart: a peer added through a link stays unconfirmed
// until the creator sees it, so its auth request is retried in the background —
// and that retry is rebuilt from the config on every start
// (restoreOutgoingAuths). If the token were not stored alongside the peer, a
// restart before the creator ever came online would leave a dead "not accepted"
// entry that no retry could revive.
//
// The restart is imitated rather than performed: every test peer gets a fresh
// data dir, but the config modifier runs before app.Init — hence before
// AuthStatus reads the config, which is the whole of what this test is about.
func TestInviteTokenSurvivesRestart(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	err := creator.api.UpdateMySettings("alice")
	ts.NoError(err)

	invite, err := creator.api.CreateInvite(entity.CreateInviteRequest{
		Alias:                "laptop",
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)
	link, err := entity.ParseInviteLink(invite.Link)
	ts.NoError(err)

	peer2 := ts.NewTestPeerWithConfig(func(conf *config.Config) {
		// What AddPeer stored before the restart, plus the fields setDefaults
		// fills in while loading a config from disk.
		conf.KnownPeers[link.PeerID] = config.KnownPeer{
			PeerID:             link.PeerID,
			Name:               link.Name,
			Alias:              link.Name,
			IPAddr:             conf.GenerateNextIpAddr(),
			DomainName:         awldns.TrimDomainName(link.Name),
			CreatedAt:          time.Now(),
			PendingInviteToken: link.Token,
		}
	})
	ts.ensurePeersAvailableInDHT(creator, peer2)

	// Connecting by hand rather than waiting for MaintainBackgroundConnections,
	// which does the same thing up to five seconds later: either way it is
	// onPeerConnected that sends the restored auth request.
	err = peer2.app.P2p.ConnectPeer(context.Background(), creator.app.P2p.PeerID())
	ts.NoError(err)

	ts.Eventually(func() bool {
		knownPeer, exists := creator.app.Conf.GetPeer(peer2.PeerID())
		return exists && knownPeer.Confirmed
	}, 15*time.Second, 50*time.Millisecond)

	peer2OnCreator, _ := creator.app.Conf.GetPeer(peer2.PeerID())
	ts.Equal("laptop", peer2OnCreator.Alias, "the restored retry carried the token, so the invite applied")
	ts.True(peer2OnCreator.WeAllowUsingAsExitNode)
	ts.Equal(invite.ID, peer2OnCreator.InviteID)

	ts.Eventually(func() bool {
		knownPeer, _ := peer2.app.Conf.GetPeer(creator.PeerID())
		return knownPeer.Confirmed && knownPeer.PendingInviteToken == ""
	}, 15*time.Second, 50*time.Millisecond)

	invites, err := creator.api.Invites()
	ts.NoError(err)
	ts.Equal(1, invites[0].UsedCount)
}

// TestAddPeerRedeemsInvite exercises the redemption invariant at the one place
// that owns it: an invite is spent inside AddPeer's critical section, so a use
// can only ever be counted together with the peer that spent it. None of these
// races can be provoked through the network, hence the direct calls.
func TestAddPeerRedeemsInvite(t *testing.T) {
	ts := NewTestSuite(t)

	creator := ts.NewTestPeer(false)
	auth := creator.app.AuthStatus

	// Two holders of a single-use link arriving together: one gets in, the rest
	// cost the link nothing.
	t.Run("Concurrent", func(t *testing.T) {
		invite, err := creator.app.Conf.CreateInvite(config.CreateInviteParams{
			MaxUses:              1,
			Alias:                "laptop",
			AllowUsingAsExitNode: true,
		})
		ts.NoError(err)

		const redeemers = 20
		peerIDs := make([]peer.ID, redeemers)
		for i := range peerIDs {
			peerIDs[i] = generateTestPeerID(ts)
		}

		var wg sync.WaitGroup
		errs := make([]error, redeemers)
		start := make(chan struct{})
		for i, peerID := range peerIDs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = auth.AddPeer(context.Background(), service.AddPeerParams{
					PeerID:            peerID,
					Name:              fmt.Sprintf("redeemer-%d", i),
					Confirmed:         true,
					UniquifyAlias:     true,
					RedeemInviteToken: invite.Token,
				})
			}()
		}
		close(start)
		wg.Wait()

		succeeded := 0
		for _, err := range errs {
			if err == nil {
				succeeded++
			}
		}
		ts.Equal(1, succeeded)

		added := make([]config.KnownPeer, 0, 1)
		for _, peerID := range peerIDs {
			if knownPeer, exists := creator.app.Conf.GetPeer(peerID.String()); exists {
				added = append(added, knownPeer)
			}
		}
		ts.Len(added, 1, "a single-use invite admits exactly one peer")
		ts.Equal("laptop", added[0].Alias, "its settings come from the invite")
		ts.True(added[0].WeAllowUsingAsExitNode)
		ts.Equal(invite.ID, added[0].InviteID)

		stored, _ := creator.app.Conf.GetInvite(invite.ID)
		ts.Equal(1, stored.UsedCount)
	})

	// A retry meeting onPeerConnected makes two auth streams from one peer; the
	// loser gets "peer has already been added" and must not burn a use.
	t.Run("AlreadyAdded", func(t *testing.T) {
		invite, err := creator.app.Conf.CreateInvite(config.CreateInviteParams{MaxUses: 2})
		ts.NoError(err)

		params := service.AddPeerParams{
			PeerID:            generateTestPeerID(ts),
			Name:              "twice",
			Confirmed:         true,
			UniquifyAlias:     true,
			RedeemInviteToken: invite.Token,
		}
		ts.NoError(auth.AddPeer(context.Background(), params))
		ts.Error(auth.AddPeer(context.Background(), params))

		stored, _ := creator.app.Conf.GetInvite(invite.ID)
		ts.Equal(1, stored.UsedCount)
	})

	// The check in the auth handler is advisory only. If the link goes bad
	// between it and the redemption, the peer is not quietly let in without what
	// the link promised.
	t.Run("Unusable", func(t *testing.T) {
		invite, err := creator.app.Conf.CreateInvite(config.CreateInviteParams{MaxUses: 1})
		ts.NoError(err)
		ts.True(creator.app.Conf.RevokeInvite(invite.ID))

		peerID := generateTestPeerID(ts)
		err = auth.AddPeer(context.Background(), service.AddPeerParams{
			PeerID:            peerID,
			Name:              "revoked",
			Confirmed:         true,
			UniquifyAlias:     true,
			RedeemInviteToken: invite.Token,
		})
		ts.ErrorIs(err, config.ErrInviteRevoked)

		_, exists := creator.app.Conf.GetPeer(peerID.String())
		ts.False(exists)
	})
}

// generateTestPeerID returns a valid peer ID for peers that never connect.
// AddPeer feeds the peer list, the DNS table and the VPN, so a fabricated
// string would be planted in all three.
func generateTestPeerID(ts *TestSuite) peer.ID {
	_, pubKey, err := crypto.GenerateEd25519Key(rand.Reader)
	ts.NoError(err)
	peerID, err := peer.IDFromPublicKey(pubKey)
	ts.NoError(err)

	return peerID
}

func TestSOCKS5ProxyFallbackToOldProtocol(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false) // client
	peer2 := ts.NewTestPeer(false) // server (simulates old version)

	ts.makeFriends(peer2, peer1)

	// Remove new protocol handler from peer2 to simulate old peer
	peer2.app.P2p.Host().RemoveStreamHandler(protocol.Socks5NoAuthMethod)

	// Wait until peer1's peerstore reflects the removal (identify-push). Otherwise
	// NewStreamMulti optimistically negotiates the stale /socks5-noauth/ against a
	// peer that no longer handles it, and the dial fails with EOF.
	ts.Eventually(func() bool {
		supported, err := peer1.app.P2p.Host().Peerstore().
			SupportsProtocols(peer2.app.P2p.PeerID(), protocol.Socks5NoAuthMethod)
		ts.NoError(err)
		return len(supported) == 0
	}, 15*time.Second, 100*time.Millisecond)

	// Allow peer1 to use peer2 as exit node
	peer1Config, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)

	err = peer2.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer1.PeerID(),
		Alias:                peer1Config.Alias,
		DomainName:           peer1Config.DomainName,
		IPAddr:               peer1Config.IPAddr,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		return peer2Config.AllowedUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	peer1.app.SOCKS5.SetProxyPeerID(peer2.PeerID())
	peer2.app.SOCKS5.SetProxyingLocalhostEnabled(true)

	// Verify SOCKS5 still works via fallback to old protocol
	testSOCKS5Proxy(ts, peer1.app.Conf.SOCKS5.ListenAddress, "")
}

func TestSOCKS5ProxyWithLocalAuth(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeerWithConfig(func(c *config.Config) {
		c.SOCKS5 = config.SOCKS5Config{
			ListenerEnabled: true,
			ProxyingEnabled: true,
			ListenAddress:   pickFreeAddr(ts.t),
			Username:        "testuser",
			Password:        "testpass",
		}
	})
	peer2 := ts.NewTestPeer(false)

	ts.makeFriends(peer2, peer1)

	// Allow peer1 to use peer2 as exit node
	peer1Config, err := peer2.api.KnownPeerConfig(peer1.PeerID())
	ts.NoError(err)

	err = peer2.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               peer1.PeerID(),
		Alias:                peer1Config.Alias,
		DomainName:           peer1Config.DomainName,
		IPAddr:               peer1Config.IPAddr,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		return peer2Config.AllowedUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)

	peer1.app.SOCKS5.SetProxyPeerID(peer2.PeerID())
	peer2.app.SOCKS5.SetProxyingLocalhostEnabled(true)

	proxyAddr := peer1.app.Conf.SOCKS5.ListenAddress

	// Correct credentials — should succeed
	testSOCKS5ProxyWithAuth(ts, proxyAddr, &proxy.Auth{User: "testuser", Password: "testpass"}, 1, "")

	// Wrong password — should fail with auth error
	testSOCKS5ProxyWithAuth(ts, proxyAddr, &proxy.Auth{User: "testuser", Password: "wrong"}, 1, "username/password authentication failed")

	// No credentials — should fail (server requires user/pass, client offers no auth only)
	testSOCKS5ProxyWithAuth(ts, proxyAddr, nil, 1, "no acceptable authentication methods")
}

func TestUpdatePeerSettingsIPAddr(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)

	// Make peer2 and peer1 friends using the helper
	ts.makeFriends(peer2, peer1)

	// Make peer3 and peer1 friends
	ts.makeFriendsWithAliases(peer3, peer1, "peer_3", "peer_1")

	time.Sleep(500 * time.Millisecond)

	// Get initial peer configurations
	peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
	ts.NoError(err)
	peer3Config, err := peer1.api.KnownPeerConfig(peer3.PeerID())
	ts.NoError(err)

	initialPeer2IP := peer2Config.IPAddr
	initialPeer3IP := peer3Config.IPAddr

	t.Run("ValidIPUpdate", func(t *testing.T) {
		newIP := "10.66.0.100"
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               newIP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Verify the IP was updated
		updatedConfig, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		ts.Equal(newIP, updatedConfig.IPAddr)

		// Restore original IP for other tests
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer2IP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)
	})

	t.Run("InvalidIPFormat", func(t *testing.T) {
		testCases := []struct {
			name     string
			ip       string
			errorMsg string
		}{
			{"empty string", "", "Field validation for 'IPAddr' failed"},
			{"invalid format", "invalid", "Field validation for 'IPAddr' failed on the 'ipv4' tag"},
			{"out of range octets", "256.1.1.1", "Field validation for 'IPAddr' failed on the 'ipv4' tag"},
			{"incomplete IP", "10.66.0", "Field validation for 'IPAddr' failed on the 'ipv4' tag"},
			{"too many octets", "10.66.0.1.1", "Field validation for 'IPAddr' failed on the 'ipv4' tag"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
					PeerID:               peer2.PeerID(),
					Alias:                peer2Config.Alias,
					DomainName:           peer2Config.DomainName,
					IPAddr:               tc.ip,
					AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
				})
				ts.Error(err)
				ts.ErrorContains(err, tc.errorMsg)
			})
		}
	})

	t.Run("IPOutsideVPNRange", func(t *testing.T) {
		testCases := []struct {
			name string
			ip   string
		}{
			{"different network", "192.168.1.1"},
			{"next network up", "10.67.0.5"},
			{"next network down", "10.65.255.255"},
			{"same class B different C", "10.67.1.1"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
					PeerID:               peer2.PeerID(),
					Alias:                peer2Config.Alias,
					DomainName:           peer2Config.DomainName,
					IPAddr:               tc.ip,
					AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
				})
				ts.Error(err)
				ts.ErrorContains(err, "IP "+tc.ip+" does not belong to subnet 10.66.0.0/16")
			})
		}
	})

	t.Run("DuplicateIPAcrossPeers", func(t *testing.T) {
		// Try to set peer2's IP to peer3's IP
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer3IP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.Error(err)
		ts.ErrorContains(err, "ip "+initialPeer3IP+" is already used by peer")

		// Verify peer2's IP wasn't changed
		unchangedConfig, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		ts.Equal(initialPeer2IP, unchangedConfig.IPAddr)
	})

	t.Run("SamePeerKeepsSameIP", func(t *testing.T) {
		// Update other settings while keeping the same IP
		newAlias := "updated_peer2"
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                newAlias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer2IP, // Same IP
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Verify the alias was updated but IP stayed the same
		updatedConfig, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		ts.Equal(newAlias, updatedConfig.Alias)
		ts.Equal(initialPeer2IP, updatedConfig.IPAddr)

		// Restore original alias
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer2IP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)
	})

	t.Run("SequentialIPUpdates", func(t *testing.T) {
		// Use a completely different free IP to avoid any conflicts
		freeIP := "10.66.0.200"

		// Update peer2: A → free IP
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               freeIP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Verify peer2 has new IP
		updatedPeer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		ts.Equal(freeIP, updatedPeer2Config.IPAddr)

		// Now update peer3 to a different free IP
		anotherFreeIP := "10.66.0.201"
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer3.PeerID(),
			Alias:                peer3Config.Alias,
			DomainName:           peer3Config.DomainName,
			IPAddr:               anotherFreeIP,
			AllowUsingAsExitNode: peer3Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Verify peer3 has the new IP
		updatedPeer3Config, err := peer1.api.KnownPeerConfig(peer3.PeerID())
		ts.NoError(err)
		ts.Equal(anotherFreeIP, updatedPeer3Config.IPAddr)

		// Now verify we can reuse the original IPs by updating back
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer2IP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer3.PeerID(),
			Alias:                peer3Config.Alias,
			DomainName:           peer3Config.DomainName,
			IPAddr:               initialPeer3IP,
			AllowUsingAsExitNode: peer3Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)
	})

	t.Run("EdgeCaseIPs", func(t *testing.T) {
		// TODO: revise .0 and .255 cases implementation

		// Test network address (.0) - currently allowed in implementation
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               "10.66.0.0",
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Test broadcast address (.255) - currently allowed in implementation
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               "10.66.0.255",
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Test valid IP at the high edge of range
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               "10.66.0.254",
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Restore original IP
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer2IP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)
	})

	t.Run("IPChangeWithTunnelPackets", func(t *testing.T) {
		const packetSize = 1500
		const packetsCount = 10
		newIP := "10.66.0.150"

		// Update peer2's IP address
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               newIP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)

		// Verify the IP was updated
		updatedConfig, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)
		ts.Equal(newIP, updatedConfig.IPAddr)

		// Configure tunnel for packet testing
		peer1.tun.SetInboundCapture(packetSize, nil)
		peer2.tun.SetInboundCapture(packetSize, nil)
		peer1.tun.ClearInboundCount()
		peer2.tun.ClearInboundCount()

		// Wait for IP change to propagate
		time.Sleep(100 * time.Millisecond)

		// Send packets from peer1 to peer2
		packet := testPacketWithDest(packetSize, newIP)
		for i := 0; i < packetsCount; i++ {
			peer1.tun.Outbound <- [][]byte{packet}
		}

		// Wait for packet processing
		time.Sleep(500 * time.Millisecond)

		// Verify packet reception
		received := peer2.tun.InboundCount()
		ts.EqualValues(packetsCount, received)

		// Restore original IP for other tests
		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               peer2.PeerID(),
			Alias:                peer2Config.Alias,
			DomainName:           peer2Config.DomainName,
			IPAddr:               initialPeer2IP,
			AllowUsingAsExitNode: peer2Config.WeAllowUsingAsExitNode,
		})
		ts.NoError(err)
	})
}

func TestDisableVPNInterface(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	// Create peer2 with disabled VPN
	peer2 := ts.NewTestPeerWithConfig(func(c *config.Config) {
		c.VPNConfig.DisableVPNInterface = true
	})

	// Verify peer2 has no VPN/Tunnel
	ts.Nil(peer2.app.vpnDevice)
	ts.Nil(peer2.app.Tunnel)
	peer2Status, err := peer2.api.PeerInfo()
	ts.NoError(err)
	ts.False(peer2Status.VPN.VPNInterfaceEnabled)
	ts.NotEmpty(peer2Status.VPN.InterfaceName)
	ts.Equal(config.DefaultVPNNetworkSubnet, peer2Status.VPN.IPNet)

	ts.makeFriends(peer2, peer1)

	// Try to send traffic from peer1 (enabled) to peer2 (disabled)
	const packetSize = 100
	peer1.tun.SetInboundCapture(packetSize, nil)
	peer2.tun.ClearInboundCount()

	// Send packet
	p2Conf, err := peer1.api.KnownPeerConfig(peer2.app.P2p.PeerID().String())
	ts.NoError(err)
	peer2IP := p2Conf.IPAddr

	packet := testPacketWithDest(packetSize, peer2IP)
	peer1.tun.Outbound <- [][]byte{packet}

	// Wait and verify nothing received on peer2's TUN
	time.Sleep(1 * time.Second)
	ts.EqualValues(0, peer2.tun.InboundCount())
}

func testSOCKS5Proxy(ts *TestSuite, proxyAddr string, expectSocksErr string) {
	testSOCKS5ProxyWithAuth(ts, proxyAddr, nil, 20, expectSocksErr)
}

func testSOCKS5ProxyWithAuth(ts *TestSuite, proxyAddr string, auth *proxy.Auth, iterations int, expectSocksErr string) {
	// setup mock server
	expectedBody := strings.Repeat("test text", 10_000)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	ts.NoError(err)
	addr := l.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, expectedBody)
	})
	//nolint
	httpServer := &http.Server{Handler: mux}
	go func() {
		_ = httpServer.Serve(l)
	}()
	defer func() {
		httpServer.Shutdown(context.Background())
	}()

	// client
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, nil)
	ts.NoError(err)
	httpTransport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
	httpClient := http.Client{Transport: httpTransport}

	// test
	for range iterations {
		response, err := httpClient.Get(fmt.Sprintf("http://%s/test", addr))
		if expectSocksErr != "" {
			ts.Error(err)

			var urlErr *url.Error
			ts.ErrorAs(err, &urlErr)
			var netErr *net.OpError
			ts.ErrorAs(urlErr.Err, &netErr)

			ts.Equal("socks connect", netErr.Op)
			ts.Contains(netErr.Err.Error(), expectSocksErr)

			continue
		}

		ts.NoError(err)
		body, err := io.ReadAll(response.Body)
		ts.NoError(err)
		err = response.Body.Close()
		ts.NoError(err)

		ts.Equal(expectedBody, string(body))
	}
}

func TestTunnelPackets(t *testing.T) {
	if israce.Enabled && runtime.GOOS == "windows" {
		t.Skip("race mode on windows is too slow for this test")
	}

	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	ts.makeFriends(peer2, peer1)

	const packetSize = 2500
	const packetsCount = 2600 // approx 1.1 p2p streams

	peer1.tun.SetInboundCapture(packetSize, nil)
	peer2.tun.SetInboundCapture(packetSize, nil)

	wg := &sync.WaitGroup{}

	sendPackets := func(peer TestPeer) {
		defer wg.Done()
		packet := testPacket(packetSize)
		packetsBatch := make([][]byte, TestTUNBatchSize)
		for i := range packetsBatch {
			packetsBatch[i] = packet
		}

		for i := 0; i < packetsCount/TestTUNBatchSize; i++ {
			peer.tun.Outbound <- packetsBatch
			// to avoid packet loss
			time.Sleep(100 * time.Millisecond)
		}
	}

	wg.Add(2)
	go sendPackets(peer1)
	go sendPackets(peer2)
	wg.Wait()

	time.Sleep(2 * time.Second)
	received1 := peer1.tun.InboundCount()
	received2 := peer2.tun.InboundCount()
	ts.EqualValues(packetsCount, received1)
	ts.EqualValues(packetsCount, received2)

	// --- IPv6 Routing Test ---
	peer2ConfigInPeer1, _ := peer1.app.Conf.GetPeer(peer2.PeerID())
	peer1ConfigInPeer2, _ := peer2.app.Conf.GetPeer(peer1.PeerID())

	peer1IPv6Str := peer1ConfigInPeer2.IPAddrV6
	peer2IPv6Str := peer2ConfigInPeer1.IPAddrV6

	ts.t.Logf("DEBUG: peer1 IPNetV6: %v", peer1.app.Conf.VPNConfig.IPNetV6)
	ts.t.Logf("DEBUG: peer1IPv6 calculated: %s, peer2IPv6 calculated: %s", peer1IPv6Str, peer2IPv6Str)

	peer1.tun.ClearInboundCount()
	peer2.tun.ClearInboundCount()

	// Send IPv6 packets from peer1 to peer2
	const ipv6PacketsCount = 10
	ipv6Packet := testPacketWithSrcDestV6(packetSize, peer1IPv6Str, peer2IPv6Str)

	for i := 0; i < ipv6PacketsCount; i++ {
		peer1.tun.Outbound <- [][]byte{ipv6Packet}
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(1 * time.Second)
	receivedIPv6 := peer2.tun.InboundCount()
	ts.EqualValues(ipv6PacketsCount, receivedIPv6, "peer2 should receive exactly %d IPv6 packets", ipv6PacketsCount)
}

func BenchmarkTunnelPackets(b *testing.B) {
	packetSizes := []int{40, 300, 800, 1300, 1800, 2300, 2800, 3500}
	for _, packetSize := range packetSizes {
		b.Run(fmt.Sprintf("%d bytes per package", packetSize), func(b *testing.B) {
			ts := NewTestSuite(b)

			peer1 := ts.NewTestPeer(true)
			peer2 := ts.NewTestPeer(true)

			ts.makeFriends(peer2, peer1)
			b.ResetTimer()

			b.SetBytes(int64(packetSize))
			packet := testPacket(packetSize)
			peer2.tun.SetInboundCapture(len(packet), nil)
			peer2.tun.ClearInboundCount()
			packetsBatch := make([][]byte, TestTUNBatchSize*10)
			for i := range packetsBatch {
				packetsBatch[i] = packet
			}

			for i := 0; i < b.N; i++ {
				peer1.tun.Outbound <- packetsBatch
				// to have packet_loss at reasonable level (but more than 0)
				const sleepEvery = 100
				if i != 0 && i%sleepEvery == 0 {
					time.Sleep(1 * time.Millisecond)
				}
			}
			received := peer2.tun.InboundCount()
			sent := peer1.tun.OutboundCount()
			packetLoss := (float64(1) - float64(received)/float64(sent)) * 100
			bandwidth := float64(received) * float64(packetSize) / 1024 / 1024
			b.ReportMetric(bandwidth, "MB/s")
			b.ReportMetric(float64(received), "packets/s")
			b.ReportMetric(packetLoss, "packet_loss")
		})
	}
}

func TestMetricsEndpoint(t *testing.T) {
	// NOTE: all tests use the same metrics register - so we can't rely on metrics values here
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(true)

	// Make some API requests first so counter metrics get populated
	_, _ = peer1.api.PeerInfo()

	// Query the /metrics endpoint
	metricsURL := fmt.Sprintf("http://%s/metrics", peer1.app.Api.Address())
	resp, err := http.Get(metricsURL) //nolint:gosec
	ts.NoError(err)
	defer resp.Body.Close()

	ts.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	ts.NoError(err)

	metricsOutput := string(body)

	// Verify AWL-specific metrics are present
	ts.Contains(metricsOutput, "awl_node_info")
	ts.Contains(metricsOutput, "awl_node_start_timestamp")
	ts.Contains(metricsOutput, "awl_node_uptime_seconds")
	ts.Contains(metricsOutput, "awl_peers_known_total")
	ts.Contains(metricsOutput, "awl_api_request_duration_seconds_bucket")

	// Verify libp2p built-in metrics are present
	ts.Contains(metricsOutput, "libp2p_")
}

func TestChooseDNSPolicy(t *testing.T) {
	const upstreamCfg = "9.9.9.9:53"
	base := []netip.Addr{netip.MustParseAddr("192.168.0.1"), netip.MustParseAddr("8.8.8.8")}

	tests := []struct {
		name             string
		forceUpstream    bool
		supportsSplitDNS bool
		base             []netip.Addr
		wantCapturesAll  bool // MatchDomains == nil
		wantUpstream     string
	}{
		{
			name:             "gateway on captures everything to configured upstream",
			forceUpstream:    true,
			supportsSplitDNS: true,
			base:             base,
			wantCapturesAll:  true,
			wantUpstream:     upstreamCfg,
		},
		{
			name:             "gateway on ignores split dns support",
			forceUpstream:    true,
			supportsSplitDNS: false,
			base:             base,
			wantCapturesAll:  true,
			wantUpstream:     upstreamCfg,
		},
		{
			name:             "split dns captures only .awl",
			forceUpstream:    false,
			supportsSplitDNS: true,
			base:             base,
			wantCapturesAll:  false,
			wantUpstream:     upstreamCfg,
		},
		{
			name:             "no split dns forwards to system base resolver",
			forceUpstream:    false,
			supportsSplitDNS: false,
			base:             base,
			wantCapturesAll:  true,
			wantUpstream:     "192.168.0.1:" + awldns.DefaultDNSPort,
		},
		{
			name:             "no split dns with empty base falls back to configured upstream",
			forceUpstream:    false,
			supportsSplitDNS: false,
			base:             nil,
			wantCapturesAll:  true,
			wantUpstream:     upstreamCfg,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matchDomains, upstream := chooseDNSPolicy(tt.forceUpstream, tt.supportsSplitDNS, tt.base, upstreamCfg)

			gotCapturesAll := matchDomains == nil
			if gotCapturesAll != tt.wantCapturesAll {
				t.Errorf("captures all queries = %v, want %v (matchDomains=%v)", gotCapturesAll, tt.wantCapturesAll, matchDomains)
			}
			if !tt.wantCapturesAll {
				// split-DNS: must capture exactly the .awl zone
				if len(matchDomains) != 1 || matchDomains[0].WithoutTrailingDot() != awldns.LocalDomain {
					t.Errorf("split-DNS match domains = %v, want [%s]", matchDomains, awldns.LocalDomain)
				}
			}
			if upstream != tt.wantUpstream {
				t.Errorf("upstream = %q, want %q", upstream, tt.wantUpstream)
			}
		})
	}
}

// dnsHandlerFunc adapts a function to service.DNSPacketHandler.
type dnsHandlerFunc func(packet []byte)

func (f dnsHandlerFunc) HandlePacket(packet []byte) { f(packet) }

// TestDNSHandlerTunnelFilter checks the Tunnel-side interceptor filter alone:
// only packets addressed to the DNS IP reach the installed handler.
func TestDNSHandlerTunnelFilter(t *testing.T) {
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(true)

	dnsIP := peer1.app.Conf.NetstackDNSIP()
	ts.NotNil(dnsIP)

	intercepted := make(chan []byte, 16)
	peer1.app.Tunnel.SetDNSHandler(dnsIP, dnsHandlerFunc(func(packet []byte) {
		intercepted <- append([]byte{}, packet...)
	}))

	dnsPacket := testPacketWithDest(0, dnsIP.String())
	// Must not reach the handler: unknown peer IP and broadcast.
	otherPacket := testPacketWithDest(0, "10.66.0.50")
	broadcastPacket := testPacketWithDest(0, "10.66.255.255")
	peer1.tun.Outbound <- [][]byte{dnsPacket, otherPacket, broadcastPacket}

	select {
	case got := <-intercepted:
		ts.Equal(dnsPacket, got)
	case <-time.After(5 * time.Second):
		ts.FailNow("dns packet was not intercepted")
	}

	select {
	case got := <-intercepted:
		_, dst := parsePacketIPs(got)
		ts.FailNowf("unexpected packet reached dns handler", "dst %s", dst)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestDNSAndroidInterceptor runs the Android DNS path end-to-end over the
// TestTUN: a raw UDP DNS query for a peer name goes into the TUN read path,
// through the interceptor into the netstack bridge and the awldns resolver,
// and the response packet comes back out of the TUN write path.
func TestDNSAndroidInterceptor(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(true)
	peer2 := ts.NewTestPeer(true)
	ts.makeFriends(peer2, peer1)

	// In production Application.Init wires this behind runtime.GOOS ==
	// "android"; the test drives the same code path directly.
	peer1.app.Dns.initDNSAndroid(peer1.app.vpnDevice, peer1.app.Tunnel)

	dnsIP := peer1.app.Conf.NetstackDNSIP()
	ts.NotNil(dnsIP)
	dnsAddress := net.JoinHostPort(dnsIP.String(), awldns.DefaultDNSPort)
	ts.Eventually(func() bool {
		return peer1.app.Dns.AwlDNSAddress() == dnsAddress
	}, 5*time.Second, 10*time.Millisecond)
	ts.True(peer1.app.Dns.IsAwlDNSSetAsSystem())
	// The running interceptor reports the same IP the config computes
	// (what gomobile's DnsServerIP relies on in the reconfigure_vpn flow).
	ts.Equal(dnsIP, peer1.app.Dns.NetstackDNSServerIP())

	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.NotEmpty(knownPeer.DomainName)

	inbound := make(chan []byte, 100)
	peer1.tun.SetInboundCapture(0, inbound)

	query := new(dns.Msg)
	query.SetQuestion(knownPeer.DomainName+".awl.", dns.TypeA)
	payload, err := query.Pack()
	ts.NoError(err)

	localIP, _ := peer1.app.Conf.VPNLocalIPMask()
	const clientPort = 40000
	queryPacket := testUDPPacket(localIP, dnsIP, clientPort, 53, payload)
	peer1.tun.Outbound <- [][]byte{queryPacket}

	timeout := time.After(10 * time.Second)
	for {
		var raw []byte
		select {
		case raw = <-inbound:
		case <-timeout:
			ts.FailNow("no DNS response arrived in TUN")
		}

		// Skip anything that is not a UDP packet from dnsIP:53 back to us.
		const udpHeaderLen = 8
		src, dst := parsePacketIPs(raw)
		ipHeaderLen := int(raw[0]&0x0f) << 2
		if raw[9] != vpn.IPProtocolUDP || len(raw) < ipHeaderLen+udpHeaderLen ||
			!src.Equal(dnsIP) || !dst.Equal(localIP) {
			continue
		}
		udpHeader := raw[ipHeaderLen:]
		ts.Equal(uint16(53), binary.BigEndian.Uint16(udpHeader[0:2]))
		ts.Equal(uint16(clientPort), binary.BigEndian.Uint16(udpHeader[2:4]))

		resp := new(dns.Msg)
		ts.NoError(resp.Unpack(udpHeader[udpHeaderLen:]))
		ts.Equal(query.Id, resp.Id)
		ts.Len(resp.Answer, 1)
		aRecord, ok := resp.Answer[0].(*dns.A)
		ts.True(ok)
		ts.Equal(knownPeer.IPAddr, aRecord.A.String())
		return
	}
}
