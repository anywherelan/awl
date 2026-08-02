package awl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
)

func TestExportServerConfiguration(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	resp, err := http.Get(fmt.Sprintf("http://%s%s", peer1.app.Api.Address(), api.ExportServerConfigPath))
	ts.NoError(err)
	defer resp.Body.Close()

	ts.Equal(http.StatusOK, resp.StatusCode)
	ts.Contains(resp.Header.Get("Content-Type"), "application/json")

	var cfg config.Config
	err = json.NewDecoder(resp.Body).Decode(&cfg)
	ts.NoError(err)
	ts.Equal(peer1.PeerID(), cfg.P2pNode.PeerID)
}

func TestUpdateMySettings(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	err := peer1.api.UpdateMySettings("new-test-name")
	ts.NoError(err)

	info, err := peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal("new-test-name", info.Name)
}

func TestGetP2pDebugInfo(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	debugInfo, err := peer1.api.P2pDebugInfo()
	ts.NoError(err)
	ts.NotEmpty(debugInfo.General.Version)
	ts.NotEmpty(debugInfo.General.Uptime)
	ts.GreaterOrEqual(debugInfo.Connections.ConnectedPeersCount, 0)
	ts.GreaterOrEqual(debugInfo.Connections.OpenConnectionsCount, 0)
	ts.GreaterOrEqual(debugInfo.Connections.OpenStreamsCount, int64(0))
	ts.NotEmpty(debugInfo.DHT.Reachability)
}

func TestGetDebugLog(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	// Generate some log entries via API calls
	for range 5 {
		_, _ = peer1.api.PeerInfo()
	}

	allLogs, err := peer1.api.ApplicationLog(0, false)
	ts.NoError(err)
	ts.NotEmpty(allLogs)

	// Request last 1 line
	oneLog, err := peer1.api.ApplicationLog(1, false)
	ts.NoError(err)
	ts.NotEmpty(oneLog)
	lines := strings.Split(strings.TrimRight(oneLog, "\n"), "\n")
	ts.Len(lines, 1)

	// Request first 1 line
	firstLog, err := peer1.api.ApplicationLog(1, true)
	ts.NoError(err)
	ts.NotEmpty(firstLog)
	firstLines := strings.Split(strings.TrimRight(firstLog, "\n"), "\n")
	ts.Len(firstLines, 1)
}

func TestSendFriendRequest_ErrorCases(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	t.Run("SelfAdd", func(t *testing.T) {
		err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: peer1.PeerID(), Alias: "self"})
		ts.Error(err)
		ts.ErrorContains(err, "You can't add yourself")
	})

	t.Run("InvalidPeerID", func(t *testing.T) {
		err := peer1.api.SendFriendRequest(entity.FriendRequest{PeerID: "not-a-valid-peer-id", Alias: "alias"})
		ts.Error(err)
		ts.ErrorContains(err, "Invalid hex-encoded multihash")
	})
}

func TestAcceptFriend_ErrorCases(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	t.Run("NoPendingRequest", func(t *testing.T) {
		err := peer1.api.ReplyFriendRequest(entity.FriendRequestReply{PeerID: peer2.PeerID(), Alias: "peer_2"})
		ts.Error(err)
		ts.ErrorContains(err, "Peer did not send you friend request")
	})

	t.Run("SelfAdd", func(t *testing.T) {
		err := peer1.api.ReplyFriendRequest(entity.FriendRequestReply{PeerID: peer1.PeerID(), Alias: "self"})
		ts.Error(err)
		ts.ErrorContains(err, "You can't add yourself")
	})

	t.Run("InvalidPeerID", func(t *testing.T) {
		err := peer1.api.ReplyFriendRequest(entity.FriendRequestReply{PeerID: "bad-peer-id", Alias: "alias"})
		ts.Error(err)
		ts.ErrorContains(err, "Invalid hex-encoded multihash")
	})
}

// TestInvitesAPI covers the invite link lifecycle through the API alone:
// creating a link, seeing it in the list and revoking it. Redeeming one takes
// two nodes and lives in application_test.go (TestAddPeerViaInviteLink and
// around it).
func TestInvitesAPI(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	err := peer1.api.UpdateMySettings("alice")
	ts.NoError(err)

	invites, err := peer1.api.Invites()
	ts.NoError(err)
	ts.Len(invites, 0)

	created, err := peer1.api.CreateInvite(entity.CreateInviteRequest{
		Label:                "my laptop",
		Alias:                "laptop",
		AllowUsingAsExitNode: true,
		ExpiresInSeconds:     int64((24 * time.Hour).Seconds()),
	})
	ts.NoError(err)
	ts.Equal(1, created.MaxUses, "MaxUses defaults to single-use")
	ts.Equal(0, created.UsedCount)
	ts.Equal(entity.InviteStatusActive, created.Status)
	ts.True(created.AllowUsingAsExitNode)
	ts.False(created.Revoked)
	ts.WithinDuration(time.Now().Add(24*time.Hour), created.ExpiresAt, time.Minute)

	link, err := entity.ParseInviteLink(created.Link)
	ts.NoError(err)
	ts.Equal(peer1.PeerID(), link.PeerID)
	ts.Equal("alice", link.Name)
	ts.NotEmpty(link.Token)

	// The token only ever leaves through the link, never as a field of its own.
	withoutLink := *created
	withoutLink.Link = ""
	body, err := json.Marshal(withoutLink)
	ts.NoError(err)
	ts.NotContains(string(body), link.Token)

	invites, err = peer1.api.Invites()
	ts.NoError(err)
	ts.Len(invites, 1)
	ts.Equal(*created, invites[0])

	err = peer1.api.RevokeInvite(created.ID)
	ts.NoError(err)

	invites, err = peer1.api.Invites()
	ts.NoError(err)
	ts.Len(invites, 1)
	ts.True(invites[0].Revoked, "revoked invites stay in the list as history")
	ts.Equal(entity.InviteStatusRevoked, invites[0].Status)

	err = peer1.api.RevokeInvite("00000000")
	ts.Error(err)
	ts.ErrorContains(err, "invite not found")
}

func TestCreateInvite_ErrorCases(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)

	t.Run("AliasWithMultiUse", func(t *testing.T) {
		_, err := peer1.api.CreateInvite(entity.CreateInviteRequest{MaxUses: 5, Alias: "laptop"})
		ts.Error(err)
		ts.ErrorContains(err, "alias can only be set for a single-use invite")
	})

	t.Run("TooManyUses", func(t *testing.T) {
		_, err := peer1.api.CreateInvite(entity.CreateInviteRequest{MaxUses: 1000})
		ts.Error(err)
	})

	t.Run("NegativeExpiry", func(t *testing.T) {
		_, err := peer1.api.CreateInvite(entity.CreateInviteRequest{ExpiresInSeconds: -1})
		ts.Error(err)
	})
}

func TestRemovePeer_PeerNotFound(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	// peer2 is not a known peer of peer1
	err := peer1.api.RemovePeer(peer2.PeerID())
	ts.Error(err)
	ts.ErrorContains(err, "peer not found")
}

func TestUpdatePeerSettings_ErrorCases(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	t.Run("PeerNotFound", func(t *testing.T) {
		err := peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:     peer2.PeerID(),
			Alias:      "peer_2",
			DomainName: "peer2.awl",
			IPAddr:     "10.66.0.2",
		})
		ts.Error(err)
		ts.ErrorContains(err, "peer not found")
	})

	t.Run("InvalidDomainName", func(t *testing.T) {
		ts.makeFriends(peer1, peer2)

		peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
		ts.NoError(err)

		err = peer1.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:     peer2.PeerID(),
			Alias:      peer2Config.Alias,
			DomainName: "invalid domain!",
			IPAddr:     peer2Config.IPAddr,
		})
		ts.Error(err)
		ts.ErrorContains(err, "invalid domain name")
	})
}
