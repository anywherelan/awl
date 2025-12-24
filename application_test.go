package awl

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/integrationtests/tools/israce"
	"go.uber.org/goleak"
	"golang.org/x/net/proxy"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
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
	ts.EqualError(err, "peer not found")
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
	err = peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
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

	// test ping
	p1Ping := peer1.app.P2p.GetPeerLatency(peer2.app.P2p.PeerID())
	ts.NotEmpty(p1Ping)
}

func TestDeclinePeerFriendRequest(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
	ts.NoError(err)

	var authRequests []entity.AuthRequest
	ts.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		ts.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.ReplyFriendRequest(authRequests[0].PeerID, "peer_1", true)
	ts.NoError(err)

	time.Sleep(500 * time.Millisecond)
	knownPeer, exists := peer1.app.Conf.GetPeer(peer2.PeerID())
	ts.True(exists)
	ts.False(knownPeer.Confirmed)
	ts.True(knownPeer.Declined)

	ts.Len(peer2.app.AuthStatus.GetIngoingAuthRequests(), 0)
	_, blockedPeerExists := peer2.app.Conf.GetBlockedPeer(peer1.PeerID())
	ts.True(blockedPeerExists)
}

func TestAutoAcceptFriendRequest(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)

	peer2.app.Conf.Lock()
	peer2.app.Conf.P2pNode.AutoAcceptAuthRequests = true
	peer2.app.Conf.Unlock()

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer_2")
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

func TestUniquePeerAlias(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)
	ts.ensurePeersAvailableInDHT(peer1, peer2)
	ts.ensurePeersAvailableInDHT(peer2, peer3)

	err := peer1.api.SendFriendRequest(peer2.PeerID(), "peer")
	ts.NoError(err)

	time.Sleep(200 * time.Millisecond)

	err = peer1.api.SendFriendRequest(peer3.PeerID(), "peer")
	ts.EqualError(err, api.ErrorPeerAliasIsNotUniq)
}

func TestUpdateUseAsExitNodeConfig(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)

	ts.makeFriends(peer2, peer1)

	current := goleak.IgnoreCurrent()
	goleak.VerifyNone(t, current)

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

	info, err = peer1.api.PeerInfo()
	ts.NoError(err)
	ts.Equal(peer2.PeerID(), info.SOCKS5.UsingPeerID)

	availableProxies, err = peer1.api.ListAvailableProxies()
	ts.NoError(err)
	ts.Len(availableProxies, 1)

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

	ts.Equal(peer1.PeerID(), peer2.app.Conf.SOCKS5.UsingPeerID)

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

func TestUpdatePeerSettingsIPAddr(t *testing.T) {
	ts := NewTestSuite(t)

	peer1 := ts.NewTestPeer(false)
	peer2 := ts.NewTestPeer(false)
	peer3 := ts.NewTestPeer(false)

	// Make peer2 and peer1 friends using the helper
	ts.makeFriends(peer2, peer1)

	// Make peer3 and peer1 friends (manual to use unique alias "peer_3")
	// TODO: refactor makeFriends helper to accept alias arg
	ts.ensurePeersAvailableInDHT(peer3, peer1)
	err := peer3.api.SendFriendRequest(peer1.PeerID(), "peer_1")
	ts.NoError(err)

	var authRequests []entity.AuthRequest
	ts.Eventually(func() bool {
		authRequests, err = peer1.api.AuthRequests()
		ts.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer1.api.ReplyFriendRequest(authRequests[0].PeerID, "peer_3", false)
	ts.NoError(err)

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
			{"same class B different C", "10.66.1.1"},
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
				ts.ErrorContains(err, "IP "+tc.ip+" does not belong to subnet 10.66.0.0/24")
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
		peer1.tun.ReferenceInboundPacketLen = packetSize
		peer2.tun.ReferenceInboundPacketLen = packetSize
		peer1.tun.ClearInboundCount()
		peer2.tun.ClearInboundCount()

		// Wait for IP change to propagate
		time.Sleep(100 * time.Millisecond)

		// Send packets from peer1 to peer2
		packet := testPacketWithDest(packetSize, newIP)
		for i := 0; i < packetsCount; i++ {
			peer1.tun.Outbound <- packet
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

func testSOCKS5Proxy(ts *TestSuite, proxyAddr string, expectSocksErr string) {
	// setup mock server
	expectedBody := strings.Repeat("test text", 10_000)
	addr := pickFreeAddr(ts.t)
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, expectedBody)
	})
	//nolint
	httpServer := &http.Server{Addr: addr, Handler: mux}
	go func() {
		_ = httpServer.ListenAndServe()
	}()
	defer func() {
		httpServer.Shutdown(context.Background())
	}()

	// client
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, nil)
	ts.NoError(err)
	httpTransport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
	httpClient := http.Client{Transport: httpTransport}

	// test
	for range 20 {
		response, err := httpClient.Get(fmt.Sprintf("http://%s/test", addr))
		if expectSocksErr != "" {
			ts.Error(err)

			var urlErr *url.Error
			ts.ErrorAs(err, &urlErr)
			var netErr *net.OpError
			ts.ErrorAs(urlErr.Err, &netErr)

			ts.Equal("socks connect", netErr.Op)
			ts.EqualError(netErr.Err, expectSocksErr)

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

	current := goleak.IgnoreCurrent()
	goleak.VerifyNone(t, current)

	const packetSize = 2500
	const packetsCount = 2600 // approx 1.1 p2p streams

	peer1.tun.ReferenceInboundPacketLen = packetSize
	peer2.tun.ReferenceInboundPacketLen = packetSize

	wg := &sync.WaitGroup{}

	sendPackets := func(peer, peerWithInbound TestPeer) {
		defer wg.Done()
		packet := testPacket(packetSize)

		for i := 0; i < packetsCount; i++ {
			peer.tun.Outbound <- packet
			// to don't have packets loss
			inbound := peerWithInbound.tun.InboundCount()
			if (int64(i) - inbound) >= 50 {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	wg.Add(2)
	go sendPackets(peer1, peer2)
	go sendPackets(peer2, peer1)
	wg.Wait()

	time.Sleep(1 * time.Second)
	received1 := peer1.tun.InboundCount()
	received2 := peer2.tun.InboundCount()
	ts.EqualValues(packetsCount, received1)
	ts.EqualValues(packetsCount, received2)
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
			var packetsSent int64
			packet := testPacket(packetSize)
			peer2.tun.ReferenceInboundPacketLen = len(packet)
			peer2.tun.ClearInboundCount()
			for i := 0; i < b.N; i++ {
				peer1.tun.Outbound <- packet
				atomic.AddInt64(&packetsSent, 1)
				// to have packet_loss at reasonable level (but more than 0)
				const sleepEvery = 100
				if i != 0 && i%sleepEvery == 0 {
					time.Sleep(1 * time.Millisecond)
				}
			}
			received := peer2.tun.InboundCount()
			sent := atomic.LoadInt64(&packetsSent)
			packetLoss := (float64(1) - float64(received)/float64(sent)) * 100
			bandwidth := float64(received) * float64(packetSize) / 1024 / 1024
			b.ReportMetric(bandwidth, "MB/s")
			b.ReportMetric(float64(received), "packets/s")
			b.ReportMetric(packetLoss, "packet_loss")
		})
	}
}
