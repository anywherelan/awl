package awl

import (
	"context"
	"testing"
	"time"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/service"
)

const (
	gatewayTestPacketSize = 500
	internetIP            = "8.8.8.8"
)

// skipIfVPNGatewayUnsupported skips tests that drive the VPN gateway runtime API
// (client/server enable) or its startup wiring. The feature is implemented only
// on Linux; on Windows and macOS the API returns an error and startup wiring is
// a no-op, so these tests don't apply there. service.VPNGatewaySupported is the
// single source of truth for platform support.
func skipIfVPNGatewayUnsupported(t *testing.T) {
	t.Helper()
	if err := service.VPNGatewaySupported(); err != nil {
		t.Skipf("VPN gateway is only supported on Linux: %v", err)
	}
}

// setupGatewayPeers creates two peers that are friends and configures them for gateway mode.
// peer1 = gateway client, peer2 = exit node.
// Returns the peers and peer1's assigned IP in peer2's config.
func setupGatewayPeers(ts *TestSuite) (client, exitNode TestPeer, clientAssignedIP string) {
	client = ts.NewTestPeer(true)
	exitNode = ts.NewTestPeer(true)

	// Configure exit node directly on tunnel (bypasses OS-level NAT setup)
	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(true)

	ts.makeFriends(client, exitNode)

	clientAssignedIP = grantExitNodePermission(ts, exitNode, client)

	// Enable gateway on client tunnel
	ts.NoError(client.app.Tunnel.SetVPNGatewayPeer(exitNode.app.P2p.PeerID()))

	return client, exitNode, clientAssignedIP
}

// grantExitNodePermission has host grant client AllowUsingAsExitNode through the
// API and waits for the flag to propagate to client's KnownPeer entry.
// Returns client's assigned IP in host's config.
func grantExitNodePermission(ts *TestSuite, host, client TestPeer) string {
	clientCfg, err := host.api.KnownPeerConfig(client.PeerID())
	ts.NoError(err)
	ts.NoError(host.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               client.PeerID(),
		Alias:                clientCfg.Alias,
		DomainName:           clientCfg.DomainName,
		IPAddr:               clientCfg.IPAddr,
		AllowUsingAsExitNode: true,
	}))
	ts.Eventually(func() bool {
		c, err := client.api.KnownPeerConfig(host.PeerID())
		ts.NoError(err)
		return c.AllowedUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond)
	return clientCfg.IPAddr
}

// captureInbound prepares a peer's TestTUN to record inbound packets of the
// standard gateway test size into a freshly-allocated channel, and resets the
// inbound counter. Returns the channel.
func captureInbound(p TestPeer, bufSize int) chan []byte {
	ch := make(chan []byte, bufSize)
	p.tun.SetInboundCapture(gatewayTestPacketSize, ch)
	p.tun.ClearInboundCount()
	return ch
}

// resetInboundCounter installs a counter-only capture (no packet channel) and
// resets the counter, for tests that assert "no packet arrived".
func resetInboundCounter(p TestPeer) {
	p.tun.SetInboundCapture(gatewayTestPacketSize, nil)
	p.tun.ClearInboundCount()
}

// expectNoInbound asserts that the peer's TestTUN does not receive any
// inbound packet within the given duration.
func expectNoInbound(ts *TestSuite, p TestPeer, dur time.Duration, msgAndArgs ...interface{}) {
	ts.Never(func() bool { return p.tun.InboundCount() > 0 }, dur, 50*time.Millisecond, msgAndArgs...)
}

// recvPacketWithTimeout reads from the InboundPackets channel with a timeout.
func recvPacketWithTimeout(ch chan []byte) ([]byte, bool) {
	select {
	case pkt := <-ch:
		return pkt, true
	case <-time.After(5 * time.Second):
		return nil, false
	}
}

// TestNormalVPNWithGateway verifies that regular peer-to-peer VPN traffic
// still works correctly when gateway mode is enabled.
func TestNormalVPNWithGateway(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	// Get exit node's IP as seen by client
	exitNodeConfig, err := client.api.KnownPeerConfig(exitNode.PeerID())
	ts.NoError(err)
	exitNodeIP := exitNodeConfig.IPAddr

	// Send normal VPN packet (to exit node's awl IP)
	exitInbound := captureInbound(exitNode, 10)

	packet := testPacketWithDest(gatewayTestPacketSize, exitNodeIP)
	client.tun.Outbound <- [][]byte{packet}

	// Verify exit node receives it with normal full IP rewrite
	rawPkt, ok := recvPacketWithTimeout(exitInbound)
	ts.True(ok, "exit node should receive normal VPN packet")

	src, dst := parsePacketIPs(rawPkt)
	// Normal VPN: src = client's assigned IP in exit node's config,
	// dst = exit node's local IP (10.66.0.1)
	clientConfig, err := exitNode.api.KnownPeerConfig(client.PeerID())
	ts.NoError(err)
	ts.Equal(clientConfig.IPAddr, src.String(), "src should be client's assigned IP (normal VPN)")
	ts.Equal("10.66.0.1", dst.String(), "dst should be local IP (normal VPN)")
}

// TestGatewayBidirectional verifies the full round-trip: client sends to internet,
// exit node receives it, then exit node sends back a return packet that reaches
// the client with the correct IP rewrites.
func TestGatewayBidirectional(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, clientAssignedIP := setupGatewayPeers(ts)

	exitInbound := captureInbound(exitNode, 10)
	clientInbound := captureInbound(client, 10)

	// 1. Client sends to internet
	outPacket := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
	client.tun.Outbound <- [][]byte{outPacket}

	// Verify exit node receives it
	rawPkt, ok := recvPacketWithTimeout(exitInbound)
	ts.True(ok, "exit node should receive outbound gateway packet")
	src, dst := parsePacketIPs(rawPkt)
	ts.Equal(clientAssignedIP, src.String())
	ts.Equal(internetIP, dst.String())

	// 2. Simulate internet reply: inject return packet at exit node
	returnPacket := testPacketWithSrcDest(gatewayTestPacketSize, internetIP, clientAssignedIP)
	exitNode.tun.Outbound <- [][]byte{returnPacket}

	// Verify client receives the reply
	rawPkt, ok = recvPacketWithTimeout(clientInbound)
	ts.True(ok, "client should receive return gateway packet")
	src, dst = parsePacketIPs(rawPkt)
	ts.Equal(internetIP, src.String())
	ts.Equal("10.66.0.1", dst.String())
}

// TestGatewayPermissionDenied covers two complementary revocation paths:
//
//  1. Defence-in-depth on the exit-node side: even if a client somehow
//     bypasses the SetGatewayPeer validation and sends gateway-style packets
//     at us, writeInboundBatch must drop them when the per-peer
//     WeAllowUsingAsExitNode flag is false. We exercise this by directly
//     flipping the flag in the exit-node config without going through the
//     API, so the change does not propagate back to the client yet.
//
//  2. Runtime API revocation propagating to the client.
//     The exit node calls UpdatePeerSettings with
//     AllowUsingAsExitNode=false; ExchangeNewStatusInfo runs in the
//     background, the client's KnownPeer.CanUseAsVPNGateway() flips to
//     false, and any new EnableGateway() call is rejected. The in-memory
//     gateway pointer on the client is intentionally NOT auto-cleared by
//     status propagation — running sessions keep their channel routing.
func TestGatewayPermissionDenied(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	// Defence-in-depth on the exit-node side: silently flip WeAllow without
	// going through the API (so the change does not propagate back to the
	// client yet) and verify writeInboundBatch drops gateway-style packets.
	t.Run("DefenceInDepthDropsAtExitNode", func(t *testing.T) {
		exitNode.app.Conf.Lock()
		clientPeer := exitNode.app.Conf.KnownPeers[client.PeerID()]
		clientPeer.WeAllowUsingAsExitNode = false
		exitNode.app.Conf.KnownPeers[client.PeerID()] = clientPeer
		exitNode.app.Conf.Unlock()

		captureInbound(exitNode, 10)

		packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
		client.tun.Outbound <- [][]byte{packet}

		expectNoInbound(ts, exitNode, 2*time.Second,
			"exit node should NOT forward packets when WeAllowUsingAsExitNode is false")
	})

	// Case #5: revoke through the proper API.
	// UpdatePeerSettings triggers ExchangeNewStatusInfo in a goroutine,
	// propagating the new AllowUsingAsExitNode value to the client; future
	// EnableGateway calls are rejected. The in-memory gateway pointer on the
	// client is intentionally NOT auto-cleared by status propagation —
	// running sessions keep their channel routing.
	t.Run("APIRevocationPropagatesToClient", func(t *testing.T) {
		clientCfg, err := exitNode.api.KnownPeerConfig(client.PeerID())
		ts.NoError(err)
		err = exitNode.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
			PeerID:               client.PeerID(),
			Alias:                clientCfg.Alias,
			DomainName:           clientCfg.DomainName,
			IPAddr:               clientCfg.IPAddr,
			AllowUsingAsExitNode: false,
		})
		ts.NoError(err)

		ts.Eventually(func() bool {
			kp, ok := client.app.Conf.GetPeer(exitNode.PeerID())
			return ok && !kp.CanUseAsVPNGateway()
		}, 15*time.Second, 100*time.Millisecond,
			"client's AllowUsingAsVPNGateway() must flip to false once revocation propagates")

		// New EnableGateway calls must be rejected.
		err = client.api.EnableVPNGatewayClient(exitNode.PeerID())
		ts.Error(err, "EnableGateway must be rejected after permission is revoked")

		// The in-memory gateway pointer is *not* auto-cleared on status
		// updates: ClientEnabled stays true until the user explicitly
		// disables it. This pins the documented behavior.
		client.app.Conf.RLock()
		ts.True(client.app.Conf.VPNGateway.ClientEnabled,
			"propagation must NOT auto-clear the client-side gateway pointer")
		client.app.Conf.RUnlock()
	})
}

// TestGatewayServerAllowsNormalAwlWithoutExitPermission is the regression
// test for a bug where a peer with serveAsVPNGateway=true would refuse
// *all* inbound tunnel packets from any peer that did not have
// WeAllowUsingAsExitNode set — including normal awl peer-to-peer traffic
// destined for the gateway machine itself. The fix moves the permission
// check inside writeInboundBatch to apply only to gateway-bound packets
// (dst outside the awl subnet); awl peer-to-peer traffic must always
// pass regardless of the exit-node permission flag.
func TestGatewayServerAllowsNormalAwlWithoutExitPermission(t *testing.T) {
	ts := NewTestSuite(t)
	client := ts.NewTestPeer(true)
	exitNode := ts.NewTestPeer(true)

	// Exit node serves as VPN gateway, but we deliberately do NOT grant the
	// client AllowUsingAsExitNode — setupGatewayPeers grants it; here we
	// inline a stripped-down version that omits that step.
	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(true)
	ts.makeFriends(client, exitNode)

	clientCfg, err := exitNode.api.KnownPeerConfig(client.PeerID())
	ts.NoError(err)
	clientAssignedIP := clientCfg.IPAddr

	exitCfg, err := client.api.KnownPeerConfig(exitNode.PeerID())
	ts.NoError(err)
	exitNodeIP := exitCfg.IPAddr

	exitInbound := captureInbound(exitNode, 10)

	// One batch with both kinds of packets:
	//   awlPkt — peer-to-peer awl traffic addressed to the gateway machine.
	//   gwPkt  — gateway-bound packet (dst is on the public internet).
	// The first must be delivered, the second must be dropped silently.
	awlPkt := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", exitNodeIP)
	gwPkt := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
	client.tun.Outbound <- [][]byte{awlPkt, gwPkt}

	rawPkt, ok := recvPacketWithTimeout(exitInbound)
	ts.True(ok, "exit node must deliver normal awl traffic even without exit-node permission")
	src, dst := parsePacketIPs(rawPkt)
	ts.Equal(clientAssignedIP, src.String(), "awl packet src should be client's assigned IP")
	ts.Equal("10.66.0.1", dst.String(), "awl packet dst should be local IP")

	// No second packet — the gateway-bound one must have been dropped.
	select {
	case extra := <-exitInbound:
		s, d := parsePacketIPs(extra)
		t.Fatalf("unexpected gateway packet leaked through: src=%s dst=%s", s, d)
	case <-time.After(1 * time.Second):
	}
}

// TestGatewayNonRoutableIPsNotForwarded verifies that packets to loopback,
// multicast, and link-local addresses are NOT forwarded through the gateway.
func TestGatewayNonRoutableIPsNotForwarded(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	resetInboundCounter(exitNode)

	nonRoutableIPs := []struct {
		name string
		ip   string
	}{
		{"loopback", "127.0.0.1"},
		{"multicast", "224.0.0.1"},
		{"link-local", "169.254.1.1"},
	}

	for _, tc := range nonRoutableIPs {
		t.Run(tc.name, func(t *testing.T) {
			exitNode.tun.ClearInboundCount()

			packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", tc.ip)
			client.tun.Outbound <- [][]byte{packet}

			expectNoInbound(ts, exitNode, 1*time.Second,
				"packet to %s should NOT be forwarded via gateway", tc.ip)
		})
	}
}

// TestGatewayAwlSubnetNotForwarded verifies that packets to AWL subnet addresses
// (10.66.0.x) that don't match any known peer are NOT forwarded through the gateway.
func TestGatewayAwlSubnetNotForwarded(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	resetInboundCounter(exitNode)

	// Send to an AWL subnet IP that doesn't belong to any known peer
	packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", "10.66.0.250")
	client.tun.Outbound <- [][]byte{packet}

	expectNoInbound(ts, exitNode, 1*time.Second,
		"packet to awl subnet should NOT be forwarded via gateway")
}

// TestGatewayPeerLifecycle verifies that gateway can be enabled, disabled,
// and re-enabled, with correct packet routing at each stage.
func TestGatewayPeerLifecycle(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	exitInbound := captureInbound(exitNode, 10)

	packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)

	// 1. Gateway enabled — packet should arrive at exit node
	client.tun.Outbound <- [][]byte{packet}
	_, ok := recvPacketWithTimeout(exitInbound)
	ts.True(ok, "with gateway enabled, packet should arrive at exit node")

	// 2. Disable gateway: multiple packets must all be dropped.
	client.app.Tunnel.ClearVPNGatewayPeer()
	exitNode.tun.ClearInboundCount()

	for i := 0; i < 5; i++ {
		client.tun.Outbound <- [][]byte{packet}
	}
	expectNoInbound(ts, exitNode, 1*time.Second,
		"with gateway disabled, internet packets should NOT arrive at exit node")

	// 3. Re-enable gateway
	ts.NoError(client.app.Tunnel.SetVPNGatewayPeer(exitNode.app.P2p.PeerID()))
	exitNode.tun.ClearInboundCount()

	client.tun.Outbound <- [][]byte{packet}
	_, ok = recvPacketWithTimeout(exitInbound)
	ts.True(ok, "after re-enabling gateway, packet should arrive at exit node")
}

// TestGatewayWithThreePeers verifies that gateway mode works correctly alongside
// normal VPN when three peers are involved: client, exit node, and a regular peer.
func TestGatewayWithThreePeers(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	// Create a third peer and make it friends with client. Distinct aliases
	// are required because the default "peer_1"/"peer_2" would collide with
	// the existing client↔exitNode pairing.
	regularPeer := ts.NewTestPeer(true)
	ts.makeFriendsWithAliases(client, regularPeer, "gw_client", "regular_peer")

	// Get IPs
	exitNodeConfig, err := client.api.KnownPeerConfig(exitNode.PeerID())
	ts.NoError(err)
	exitNodeIP := exitNodeConfig.IPAddr

	regularPeerConfig, err := client.api.KnownPeerConfig(regularPeer.PeerID())
	ts.NoError(err)
	regularPeerIP := regularPeerConfig.IPAddr

	// Test 1: Normal VPN to regular peer works.
	//
	// Also covers case #8: when gateway is enabled,
	// traffic to another awl peer must take the per-peer awl path, not the
	// gateway path — the exit node must NOT see the packet.
	t.Run("NormalVPNToRegularPeer", func(t *testing.T) {
		regularInbound := captureInbound(regularPeer, 10)
		// Counter-only capture on the exit node so we can assert it sees nothing.
		resetInboundCounter(exitNode)

		packet := testPacketWithDest(gatewayTestPacketSize, regularPeerIP)
		client.tun.Outbound <- [][]byte{packet}

		rawPkt, ok := recvPacketWithTimeout(regularInbound)
		ts.True(ok, "regular peer should receive normal VPN packet")

		_, dst := parsePacketIPs(rawPkt)
		ts.Equal("10.66.0.1", dst.String(), "dst should be local IP (normal VPN)")

		// Case #8: even with gateway on, awl-subnet traffic must not be
		// duplicated through the exit node.
		expectNoInbound(ts, exitNode, 500*time.Millisecond,
			"exit node TUN must NOT receive a packet destined for another awl peer")
	})

	// Test 2: Normal VPN to exit node works (as awl peer, not gateway)
	t.Run("NormalVPNToExitNode", func(t *testing.T) {
		exitInbound := captureInbound(exitNode, 10)

		packet := testPacketWithDest(gatewayTestPacketSize, exitNodeIP)
		client.tun.Outbound <- [][]byte{packet}

		rawPkt, ok := recvPacketWithTimeout(exitInbound)
		ts.True(ok, "exit node should receive normal VPN packet")

		_, dst := parsePacketIPs(rawPkt)
		ts.Equal("10.66.0.1", dst.String(), "dst should be local IP (normal VPN)")
	})

	// Test 3: Gateway traffic goes to exit node, not regular peer
	t.Run("GatewayTrafficToExitNode", func(t *testing.T) {
		exitInbound := captureInbound(exitNode, 10)
		resetInboundCounter(regularPeer)

		packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
		client.tun.Outbound <- [][]byte{packet}

		_, ok := recvPacketWithTimeout(exitInbound)
		ts.True(ok, "exit node should receive gateway packet")

		ts.EqualValues(0, regularPeer.tun.InboundCount(),
			"regular peer should NOT receive gateway traffic")
	})

	// Test 4: Regular peer can still send to client
	t.Run("RegularPeerToClient", func(t *testing.T) {
		// Get client's IP from regular peer's perspective
		clientConfig, err := regularPeer.api.KnownPeerConfig(client.PeerID())
		ts.NoError(err)

		clientInbound := captureInbound(client, 10)

		packet := testPacketWithDest(gatewayTestPacketSize, clientConfig.IPAddr)
		regularPeer.tun.Outbound <- [][]byte{packet}

		rawPkt, ok := recvPacketWithTimeout(clientInbound)
		ts.True(ok, "client should receive packet from regular peer")

		_, dst := parsePacketIPs(rawPkt)
		ts.Equal("10.66.0.1", dst.String(), "dst should be client's local IP")
	})
}

// TestGatewayMultipleDestinations verifies that packets to multiple distinct
// internet IPs (in both directions) are all correctly forwarded through the
// gateway with the right rewrite applied per-direction.
func TestGatewayMultipleDestinations(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, clientAssignedIP := setupGatewayPeers(ts)

	ips := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "142.250.80.46"}

	t.Run("Outbound", func(t *testing.T) {
		exitInbound := captureInbound(exitNode, 20)

		for _, destIP := range ips {
			packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", destIP)
			client.tun.Outbound <- [][]byte{packet}
		}

		for _, expectedDst := range ips {
			rawPkt, ok := recvPacketWithTimeout(exitInbound)
			ts.True(ok, "exit node should receive packet for %s", expectedDst)

			src, dst := parsePacketIPs(rawPkt)
			ts.Equal(clientAssignedIP, src.String())
			ts.Equal(expectedDst, dst.String())
		}
	})

	t.Run("Return", func(t *testing.T) {
		clientInbound := captureInbound(client, 20)

		for _, srcIP := range ips {
			pkt := testPacketWithSrcDest(gatewayTestPacketSize, srcIP, clientAssignedIP)
			exitNode.tun.Outbound <- [][]byte{pkt}
		}

		for _, expectedSrc := range ips {
			rawPkt, ok := recvPacketWithTimeout(clientInbound)
			ts.True(ok, "client should receive return packet from %s", expectedSrc)

			src, dst := parsePacketIPs(rawPkt)
			ts.Equal(expectedSrc, src.String(), "src should be preserved")
			ts.Equal("10.66.0.1", dst.String(), "dst should be rewritten to local IP")
		}
	})
}

// TestGatewayExitNodeNotServing verifies that when an exit node turns its
// serveAsVPNGateway runtime flag off, Forward-tagged packets are dropped on
// receive (metric: gateway_server_disabled) instead of being silently
// rewritten as normal awl traffic. The client still has its gateway pointer
// set and stamps Forward on outbound packets; the exit-node-side switch in
// writeInboundBatch refuses to handle them without the server role.
func TestGatewayExitNodeNotServing(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(false)

	resetInboundCounter(exitNode)

	packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
	client.tun.Outbound <- [][]byte{packet}

	expectNoInbound(ts, exitNode, 500*time.Millisecond,
		"Forward packet must be dropped on exit node when server mode is off")
}

// TestGatewayMixedBatch verifies that a batch combining awl-subnet packets
// and gateway (non-awl) packets gets the correct per-packet rewrite in both
// directions: client → exit node (Outbound) and exit node → client (Return).
func TestGatewayMixedBatch(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, clientAssignedIP := setupGatewayPeers(ts)

	t.Run("Outbound", func(t *testing.T) {
		// Get exit node's IP from client's perspective
		exitNodeConfig, err := client.api.KnownPeerConfig(exitNode.PeerID())
		ts.NoError(err)
		exitNodeIP := exitNodeConfig.IPAddr

		exitInbound := captureInbound(exitNode, 20)

		// Send a normal awl packet and a gateway packet in one batch.
		awlPacket := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", exitNodeIP)
		gwPacket := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
		client.tun.Outbound <- [][]byte{awlPacket, gwPacket}

		rawPkt1, ok := recvPacketWithTimeout(exitInbound)
		ts.True(ok, "exit node should receive first packet")
		rawPkt2, ok := recvPacketWithTimeout(exitInbound)
		ts.True(ok, "exit node should receive second packet")

		// Classify by dst: awl-subnet (full rewrite) vs internet (src-only).
		var normalPkt, gatewayPkt []byte
		for _, pkt := range [][]byte{rawPkt1, rawPkt2} {
			_, dst := parsePacketIPs(pkt)
			if dst.String() == "10.66.0.1" {
				normalPkt = pkt
			} else {
				gatewayPkt = pkt
			}
		}
		ts.NotNil(normalPkt, "should have a normal awl packet")
		ts.NotNil(gatewayPkt, "should have a gateway packet")

		// Normal packet: full rewrite (src=clientAssignedIP, dst=10.66.0.1)
		src, dst := parsePacketIPs(normalPkt)
		ts.Equal(clientAssignedIP, src.String(), "normal packet src should be client's assigned IP")
		ts.Equal("10.66.0.1", dst.String(), "normal packet dst should be local IP")

		// Gateway packet: src-only rewrite (src=clientAssignedIP, dst preserved)
		src, dst = parsePacketIPs(gatewayPkt)
		ts.Equal(clientAssignedIP, src.String(), "gateway packet src should be client's assigned IP")
		ts.Equal(internetIP, dst.String(), "gateway packet dst should be preserved")
	})

	t.Run("Return", func(t *testing.T) {
		clientInbound := captureInbound(client, 20)

		// Get exit node's assigned IP in client's config
		exitNodeConfig, err := client.api.KnownPeerConfig(exitNode.PeerID())
		ts.NoError(err)
		exitNodeIPInClient := exitNodeConfig.IPAddr

		// Inject at exit node's TUN one gateway-return and one normal awl packet,
		// both addressed to the client's assigned IP.
		gwReturnPkt := testPacketWithSrcDest(gatewayTestPacketSize, internetIP, clientAssignedIP)
		normalPkt := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", clientAssignedIP)
		exitNode.tun.Outbound <- [][]byte{gwReturnPkt, normalPkt}

		rawPkt1, ok := recvPacketWithTimeout(clientInbound)
		ts.True(ok, "client should receive first packet")
		rawPkt2, ok := recvPacketWithTimeout(clientInbound)
		ts.True(ok, "client should receive second packet")

		// Classify by src: internet (dst-only rewrite) vs awl (full rewrite).
		var returnPktData, normalPktData []byte
		for _, pkt := range [][]byte{rawPkt1, rawPkt2} {
			src, _ := parsePacketIPs(pkt)
			if src.String() == internetIP {
				returnPktData = pkt
			} else {
				normalPktData = pkt
			}
		}
		ts.NotNil(returnPktData, "should have a gateway return packet")
		ts.NotNil(normalPktData, "should have a normal awl packet")

		// Gateway return: dst-only rewrite (src preserved, dst=10.66.0.1)
		src, dst := parsePacketIPs(returnPktData)
		ts.Equal(internetIP, src.String(), "gateway return src should be preserved")
		ts.Equal("10.66.0.1", dst.String(), "gateway return dst should be local IP")

		// Normal awl: full rewrite (src=exitNodeIPInClient, dst=10.66.0.1)
		src, dst = parsePacketIPs(normalPktData)
		ts.Equal(exitNodeIPInClient, src.String(), "normal packet src should be exit node's assigned IP")
		ts.Equal("10.66.0.1", dst.String(), "normal packet dst should be local IP")
	})
}

// TestGatewayAPIEnableUnknownPeer verifies that enabling gateway with an
// unknown peer ID returns an error.
func TestGatewayAPIEnableUnknownPeer(t *testing.T) {
	ts := NewTestSuite(t)
	client := ts.NewTestPeer(true)

	err := client.api.EnableVPNGatewayClient("QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N")
	ts.Error(err)
}

// TestGatewayAPIEnableNotAllowed verifies that enabling gateway with a peer
// that doesn't allow exit node usage returns an error.
func TestGatewayAPIEnableNotAllowed(t *testing.T) {
	ts := NewTestSuite(t)
	client := ts.NewTestPeer(true)
	peer2 := ts.NewTestPeer(true)
	ts.makeFriends(client, peer2)

	// peer2 does NOT have AllowedUsingAsExitNode set
	err := client.api.EnableVPNGatewayClient(peer2.PeerID())
	ts.Error(err)
}

// TestGatewayPeerInfoStatus verifies that gateway state is reflected in
// /settings/peer_info (the canonical status surface — there is no dedicated
// /gateway/status endpoint).
func TestGatewayPeerInfoStatus(t *testing.T) {
	skipIfVPNGatewayUnsupported(t)
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	err := client.api.EnableVPNGatewayClient(exitNode.PeerID())
	ts.NoError(err)

	info, err := client.api.PeerInfo()
	ts.NoError(err)
	ts.True(info.VPNGateway.ClientEnabled)
	ts.Equal(exitNode.PeerID(), info.VPNGateway.GatewayPeerID)
	ts.True(info.VPNGateway.Connected, "exit node should be connected")
}

// TestGatewayAPIListAvailableGateways verifies the list available gateways API endpoint.
func TestGatewayAPIListAvailableGateways(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	gateways, err := client.api.ListAvailableVPNGateways()
	ts.NoError(err)

	// exitNode has AllowedUsingAsExitNode set from setupGatewayPeers
	ts.Len(gateways, 1)
	ts.Equal(exitNode.PeerID(), gateways[0].PeerID)
	ts.True(gateways[0].Connected)
}

// TestGatewayAPIRuntimeToggle verifies that EnableGateway and DisableGateway
// take effect immediately (no restart): each call must update Tunnel state
// and flip Application gateway-route bookkeeping. It also covers the
// atomic-switch case: an enable with a different exit-node ID while already
// enabled must rebind the tunnel without tearing routes down and back up.
func TestGatewayAPIRuntimeToggle(t *testing.T) {
	skipIfVPNGatewayUnsupported(t)
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	checkClientBound := func(want bool) {
		t.Helper()
		ts.Equal(want, client.app.VPNGateway.IsClientActive(), "client gateway routes installed")
	}

	// setupGatewayPeers calls Tunnel.SetVPNGatewayPeer directly without going
	// through the API — at this point routes are NOT applied yet.
	checkClientBound(false)

	ts.NoError(client.api.EnableVPNGatewayClient(exitNode.PeerID()))
	checkClientBound(true)

	// Idempotent: a second enable with the same peer must not double-apply.
	// The route-state pointer must be reused.
	routeStateBefore := client.app.VPNGateway.ClientRouteState()
	ts.NoError(client.api.EnableVPNGatewayClient(exitNode.PeerID()))
	ts.Same(routeStateBefore, client.app.VPNGateway.ClientRouteState(), "route state must be reused on re-enable with same peer")
	client.app.Conf.RLock()
	ts.Equal(exitNode.PeerID(), client.app.Conf.VPNGateway.GatewayPeerID, "config still points to original peer")
	client.app.Conf.RUnlock()

	ts.NoError(client.api.DisableVPNGatewayClient())
	checkClientBound(false)
	client.app.Conf.RLock()
	ts.False(client.app.Conf.VPNGateway.ClientEnabled)
	client.app.Conf.RUnlock()

	// Re-enable works on the same Application instance.
	ts.NoError(client.api.EnableVPNGatewayClient(exitNode.PeerID()))
	checkClientBound(true)

	// Atomic switch to a second exit node: spin up a third peer, register
	// it as a valid exit node for the client, then call EnableGateway with
	// the new peer ID *while gateway is already on*. The route state
	// pointer must survive (no teardown) and the tunnel/config bindings
	// must rebind to the new peer.
	exitNode2 := ts.NewTestPeer(true)
	exitNode2.app.Tunnel.SetVPNGatewayServerEnabled(true)
	// Custom aliases: ts.makeFriends defaults to "peer_1"/"peer_2" which
	// would collide with the first exitNode pairing.
	ts.makeFriendsWithAliases(client, exitNode2, "client_alt", "peer_3")

	grantExitNodePermission(ts, exitNode2, client)

	routeStatePreSwitch := client.app.VPNGateway.ClientRouteState()
	ts.NoError(client.api.EnableVPNGatewayClient(exitNode2.PeerID()))
	ts.Same(routeStatePreSwitch, client.app.VPNGateway.ClientRouteState(), "atomic switch must not reinstall routes")
	client.app.Conf.RLock()
	ts.Equal(exitNode2.PeerID(), client.app.Conf.VPNGateway.GatewayPeerID, "config must point to new exit node")
	client.app.Conf.RUnlock()
}

// TestGatewayAPIExitNodeMode verifies that the /gateway/exit_node endpoint
// applies and tears down the server-side state at runtime, persisting the
// flag and propagating it to peers via the next status exchange.
func TestGatewayAPIExitNodeMode(t *testing.T) {
	skipIfVPNGatewayUnsupported(t)
	ts := NewTestSuite(t)
	peer1 := ts.NewTestPeer(true)
	peer2 := ts.NewTestPeer(true)
	ts.makeFriends(peer1, peer2)

	// Initially both have ServeAsVPNGateway=false.
	peer1.app.Conf.RLock()
	ts.False(peer1.app.Conf.VPNGateway.ServerEnabled)
	peer1.app.Conf.RUnlock()
	ts.False(peer1.app.VPNGateway.IsServerActive())

	// Turn it on via API.
	ts.NoError(peer1.api.SetVPNGatewayServerEnabled(true))
	peer1.app.Conf.RLock()
	ts.True(peer1.app.Conf.VPNGateway.ServerEnabled)
	peer1.app.Conf.RUnlock()
	ts.True(peer1.app.VPNGateway.IsServerActive(), "NAT state must be tracked after enable")

	// Idempotent.
	ts.NoError(peer1.api.SetVPNGatewayServerEnabled(true))
	ts.True(peer1.app.VPNGateway.IsServerActive())

	// peer2 should learn that peer1 serves as VPN gateway. The background
	// status exchange runs every 5 minutes, which is too slow for a test —
	// trigger it manually to force the next exchange immediately.
	peer1.app.AuthStatus.ExchangeStatusInfoWithAllKnownPeers(peer1.app.Ctx())
	ts.Eventually(func() bool {
		cfg, err := peer2.api.KnownPeerConfig(peer1.PeerID())
		ts.NoError(err)
		return cfg.RemoteVPNGatewayServerEnabled
	}, 15*time.Second, 100*time.Millisecond, "peer2 must observe peer1 advertising VPN gateway")

	// Turn it off; NAT teardown + flag clears.
	ts.NoError(peer1.api.SetVPNGatewayServerEnabled(false))
	peer1.app.Conf.RLock()
	ts.False(peer1.app.Conf.VPNGateway.ServerEnabled)
	peer1.app.Conf.RUnlock()
	ts.False(peer1.app.VPNGateway.IsServerActive(), "NAT state must be cleared after disable")
}

// TestGatewayBroadcastHandledViaBroadcastPath verifies that broadcast packets
// (255.255.255.255) go through the existing broadcast code path, not the
// gateway path. On the exit node side, the broadcast arrives via the peer's
// normal inbound channel and the src is rewritten to the client's assigned IP.
func TestGatewayBroadcastHandledViaBroadcastPath(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, clientAssignedIP := setupGatewayPeers(ts)

	exitInbound := captureInbound(exitNode, 10)

	packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", "255.255.255.255")
	client.tun.Outbound <- [][]byte{packet}

	// Exit node receives it — the broadcast code path sends it to all peers
	rawPkt, ok := recvPacketWithTimeout(exitInbound)
	ts.True(ok, "exit node should receive broadcast packet")

	src, _ := parsePacketIPs(rawPkt)
	ts.Equal(clientAssignedIP, src.String(), "broadcast src should be rewritten to client's assigned IP")
}

// TestGatewaySetGatewayPeerNotAllowed verifies that SetGatewayPeer rejects
// peers whose AllowUsingAsVPNGateway() is false (i.e. the peer either does
// not advertise VPN gateway service, or has not granted us exit-node use).
// Each missing flag is exercised independently.
func TestGatewaySetGatewayPeerNotAllowed(t *testing.T) {
	ts := NewTestSuite(t)
	client := ts.NewTestPeer(true)
	other := ts.NewTestPeer(true)
	ts.makeFriends(client, other)

	cases := []struct {
		name              string
		serveAsGateway    bool
		allowAsExit       bool
		expectErrContains string
	}{
		{
			name:              "AllowedExitButNotServing",
			serveAsGateway:    false,
			allowAsExit:       true,
			expectErrContains: "RemoteVPNGatewayServerEnabled=false",
		},
		{
			name:              "ServingButExitNotAllowed",
			serveAsGateway:    true,
			allowAsExit:       false,
			expectErrContains: "AllowedUsingAsExitNode=false",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Inject the desired KnownPeer flags directly so we don't have to
			// orchestrate full status propagation for each case.
			client.app.Conf.Lock()
			kp := client.app.Conf.KnownPeers[other.PeerID()]
			kp.AllowedUsingAsExitNode = tc.allowAsExit
			kp.RemoteVPNGatewayServerEnabled = tc.serveAsGateway
			client.app.Conf.KnownPeers[other.PeerID()] = kp
			client.app.Conf.Unlock()

			err := client.app.Tunnel.SetVPNGatewayPeer(other.app.P2p.PeerID())
			ts.Error(err)
			ts.Contains(err.Error(), tc.expectErrContains)
		})
	}
}

// TestGatewayPeerLifecycleConfigPersisted verifies that SetGatewayPeer and
// ClearGatewayPeer write the choice to the config under a single critical
// section, not just runtime state. This is what allows the API/CLI handlers
// to delegate to the service layer instead of maintaining their own writes.
func TestGatewayPeerLifecycleConfigPersisted(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	// setupGatewayPeers already enabled gateway via SetGatewayPeer.
	client.app.Conf.RLock()
	ts.True(client.app.Conf.VPNGateway.ClientEnabled, "SetGatewayPeer must persist Enabled=true")
	ts.Equal(exitNode.PeerID(), client.app.Conf.VPNGateway.GatewayPeerID)
	client.app.Conf.RUnlock()

	client.app.Tunnel.ClearVPNGatewayPeer()
	client.app.Conf.RLock()
	ts.False(client.app.Conf.VPNGateway.ClientEnabled, "ClearGatewayPeer must persist Enabled=false")
	ts.Equal("", client.app.Conf.VPNGateway.GatewayPeerID)
	client.app.Conf.RUnlock()
}

// TestGatewayServeAsVPNGatewayConfigPersisted verifies that
// SetServeAsVPNGateway writes the choice to the config so it survives a
// restart and propagates on the next status exchange.
func TestGatewayServeAsVPNGatewayConfigPersisted(t *testing.T) {
	ts := NewTestSuite(t)
	exitNode := ts.NewTestPeer(true)

	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(true)
	exitNode.app.Conf.RLock()
	ts.True(exitNode.app.Conf.VPNGateway.ServerEnabled,
		"SetServeAsVPNGateway(true) must persist ServeAsVPNGateway=true")
	exitNode.app.Conf.RUnlock()

	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(false)
	exitNode.app.Conf.RLock()
	ts.False(exitNode.app.Conf.VPNGateway.ServerEnabled,
		"SetServeAsVPNGateway(false) must persist ServeAsVPNGateway=false")
	exitNode.app.Conf.RUnlock()
}

// TestGatewayServeAsVPNGatewayPropagatesViaStatus verifies the new propagated
// status field: when the exit node enables VPN gateway service, the client's
// KnownPeer entry for that peer eventually has RemoteVPNGatewayServerEnabled=true.
// AllowUsingAsVPNGateway() then becomes the AND of this with the existing
// AllowedUsingAsExitNode (shared with SOCKS5).
func TestGatewayServeAsVPNGatewayPropagatesViaStatus(t *testing.T) {
	ts := NewTestSuite(t)
	client := ts.NewTestPeer(true)
	exitNode := ts.NewTestPeer(true)
	ts.makeFriends(client, exitNode)

	// Initially exit node is not serving — propagation should set false.
	ts.Eventually(func() bool {
		kp, _ := client.app.Conf.GetPeer(exitNode.PeerID())
		return !kp.RemoteVPNGatewayServerEnabled
	}, 15*time.Second, 100*time.Millisecond,
		"client should initially see RemoteVPNGatewayServerEnabled=false")

	// Flip it on and trigger a fresh status exchange so the new value
	// propagates without waiting for the 5-minute background ticker.
	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(true)
	exitKnownByExit, _ := exitNode.app.Conf.GetPeer(client.PeerID())
	ts.NoError(exitNode.app.AuthStatus.ExchangeNewStatusInfo(
		context.Background(), client.app.P2p.PeerID(), exitKnownByExit))

	ts.Eventually(func() bool {
		kp, _ := client.app.Conf.GetPeer(exitNode.PeerID())
		return kp.RemoteVPNGatewayServerEnabled
	}, 15*time.Second, 100*time.Millisecond,
		"client should see RemoteVPNGatewayServerEnabled=true after propagation")

	// And the field is exposed via /peers/get_known so the Flutter
	// exit-node picker can classify peers without per-peer fan-out.
	peers, err := client.api.KnownPeers()
	ts.NoError(err)
	ts.Len(peers, 1)
	ts.Equal(exitNode.PeerID(), peers[0].PeerID)
	ts.True(peers[0].RemoteVPNGatewayServerEnabled, "list endpoint must surface RemoteVPNGatewayServerEnabled")
}

// TestGatewayRebindOnPeerReadd covers the full rebind cycle: removing the
// configured exit node from KnownPeers must clear in-memory gateway state
// (and reject subsequent SetGatewayPeer for the now-unknown peer); re-adding
// the same peer must let SetGatewayPeer succeed again and resume packet flow.
// HandleReadPackets must not panic on packets sent during the gap.
func TestGatewayRebindOnPeerReadd(t *testing.T) {
	ts := NewTestSuite(t)
	client, exitNode, _ := setupGatewayPeers(ts)

	// Snapshot the KnownPeer entry so we can re-add it later with the same
	// permissions intact.
	originalPeer, ok := client.app.Conf.GetPeer(exitNode.PeerID())
	ts.True(ok)
	ts.True(originalPeer.CanUseAsVPNGateway(),
		"precondition: setupGatewayPeers must leave the peer as a valid VPN gateway target")

	// Sanity: gateway packets flow before we touch anything.
	exitInbound := captureInbound(exitNode, 10)
	packet := testPacketWithSrcDest(gatewayTestPacketSize, "10.66.0.1", internetIP)
	client.tun.Outbound <- [][]byte{packet}
	_, ok = recvPacketWithTimeout(exitInbound)
	ts.True(ok, "gateway should flow before peer removal")

	// Remove the exit node from KnownPeers. UpsertPeer/RemovePeer emit
	// KnownPeerChanged which triggers an async RefreshPeersList; we also
	// call it explicitly for synchronous determinism.
	client.app.Conf.RemovePeer(exitNode.PeerID())
	client.app.Tunnel.RefreshPeersList()

	err := client.app.Tunnel.SetVPNGatewayPeer(exitNode.app.P2p.PeerID())
	ts.Error(err, "SetGatewayPeer must reject a peer that is no longer in KnownPeers")
	ts.Contains(err.Error(), "is not in known peers")

	// Re-add the peer with the original (gateway-eligible) flags.
	// RefreshPeersList rebuilds the VpnPeer; SetGatewayPeer must succeed.
	client.app.Conf.UpsertPeer(originalPeer)
	client.app.Tunnel.RefreshPeersList()

	ts.NoError(client.app.Tunnel.SetVPNGatewayPeer(exitNode.app.P2p.PeerID()),
		"SetGatewayPeer must succeed after the peer is re-added")

	// Verify gateway packets flow again. ClearInboundCount + a fresh
	// capture in case the previous run left leftover state.
	exitInbound = captureInbound(exitNode, 10)
	client.tun.Outbound <- [][]byte{packet}
	_, ok = recvPacketWithTimeout(exitInbound)
	ts.True(ok, "gateway should flow again after the peer is re-added")
}

// TestGatewayListAvailableGatewaysFiltersToVPNGatewayOnly verifies that
// /gateway/list_available returns only peers where
// KnownPeer.CanUseAsVPNGateway() is true. SOCKS5-only exit nodes
// (AllowedUsingAsExitNode=true but RemoteVPNGatewayServerEnabled=false) and
// peers without exit-node permission must be excluded.
func TestGatewayListAvailableGatewaysFiltersToVPNGatewayOnly(t *testing.T) {
	ts := NewTestSuite(t)
	client := ts.NewTestPeer(true)

	// Peer A: VPN-gateway-capable (will get both flags).
	vpnExit := ts.NewTestPeer(true)
	vpnExit.app.Tunnel.SetVPNGatewayServerEnabled(true)

	// Peer B: SOCKS5-only exit (AllowedUsingAsExitNode=true but
	// ServeAsVPNGateway=false — the kind of peer the picker must skip).
	socks5Exit := ts.NewTestPeer(true)

	// Peer C: friend with no exit-node permission. Negative control.
	plainPeer := ts.NewTestPeer(true)

	// Build friendships individually with unique aliases — default
	// "peer_1"/"peer_2" would collide across three handshakes.
	ts.makeFriendsWithAliases(client, vpnExit, "client", "vpn_exit")
	ts.makeFriendsWithAliases(client, socks5Exit, "client", "socks5_exit")
	ts.makeFriendsWithAliases(client, plainPeer, "client", "plain_peer")

	// Allow vpnExit and socks5Exit to be used as exit nodes. grantExitNodePermission
	// also waits for AllowedUsingAsExitNode to propagate back to the client.
	grantExitNodePermission(ts, vpnExit, client)
	grantExitNodePermission(ts, socks5Exit, client)

	// Wait for ServeAsVPNGateway / non-exit-node flags to also reach the
	// expected combinations on the client side.
	ts.Eventually(func() bool {
		kpVPN, ok1 := client.app.Conf.GetPeer(vpnExit.PeerID())
		kpSocks5, ok2 := client.app.Conf.GetPeer(socks5Exit.PeerID())
		kpPlain, ok3 := client.app.Conf.GetPeer(plainPeer.PeerID())
		return ok1 && ok2 && ok3 &&
			kpVPN.CanUseAsVPNGateway() &&
			kpSocks5.AllowedUsingAsExitNode && !kpSocks5.RemoteVPNGatewayServerEnabled &&
			!kpPlain.AllowedUsingAsExitNode
	}, 15*time.Second, 100*time.Millisecond,
		"status flags didn't propagate to the expected combinations")

	gateways, err := client.api.ListAvailableVPNGateways()
	ts.NoError(err)
	ts.Len(gateways, 1,
		"ListAvailableGateways must include only peers that AllowUsingAsVPNGateway()")
	ts.Equal(vpnExit.PeerID(), gateways[0].PeerID)
}

// TestGatewayInvalidPeerIDAtStartupClearsGracefully covers the only graceful
// path in VPNGateway.SetupAtStartup: a persisted GatewayPeerID that fails
// peer.Decode (malformed string). In that case the setup logs a warning,
// calls DisableClient to wipe the bad config entries, and lets Init complete
// normally.
//
// Any other failure during startup (valid-format peer ID but not in
// KnownPeers, OS-level apply failure, etc.) propagates from EnableClient and
// fails Init — see TestGatewayUnknownPeerIDAtStartupFailsBoot.
func TestGatewayInvalidPeerIDAtStartupClearsGracefully(t *testing.T) {
	skipIfVPNGatewayUnsupported(t)
	ts := NewTestSuite(t)

	// Malformed peer ID: not a valid multihash, so peer.Decode returns an
	// error before EnableClient is ever reached.
	const malformedPeerID = "not-a-valid-peer-id"

	tp := ts.NewTestPeerWithAppConfig(
		func(c *config.Config) {
			c.VPNGateway.ClientEnabled = true
			c.VPNGateway.GatewayPeerID = malformedPeerID
		},
		nil,
	)

	tp.app.Conf.RLock()
	defer tp.app.Conf.RUnlock()
	ts.False(tp.app.Conf.VPNGateway.ClientEnabled,
		"malformed GatewayPeerID must be cleared at startup along with ClientEnabled")
	ts.Equal("", tp.app.Conf.VPNGateway.GatewayPeerID,
		"malformed GatewayPeerID must be cleared at startup")
}

// TestGatewayUnknownPeerIDAtStartupFailsBoot pins down the symmetric case to
// TestGatewayInvalidPeerIDAtStartupClearsGracefully: a valid-format peer ID
// that is not in KnownPeers is NOT a recoverable startup error. EnableClient
// returns "peer ... is not in known peers" from Tunnel.SetVPNGatewayPeer,
// SetupAtStartup wraps it, and Init fails. The persisted config is left
// untouched so a human can investigate why the peer disappeared.
func TestGatewayUnknownPeerIDAtStartupFailsBoot(t *testing.T) {
	skipIfVPNGatewayUnsupported(t)
	ts := NewTestSuite(t)

	// Valid-format peer ID that won't be in KnownPeers.
	const unknownPeerID = "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"

	tp, err := ts.NewTestPeerExpectingInitError(
		func(c *config.Config) {
			c.VPNGateway.ClientEnabled = true
			c.VPNGateway.GatewayPeerID = unknownPeerID
		},
		nil,
	)
	ts.Error(err, "Init must fail when GatewayPeerID is valid format but not in KnownPeers")
	ts.Contains(err.Error(), "not in known peers",
		"underlying error must come from Tunnel.SetVPNGatewayPeer")

	// Config must be left as-is — only DisableClient (the malformed-ID path)
	// wipes it; the unknown-peer path bubbles up the error so an operator
	// can decide what to do.
	ts.True(tp.app.Conf.VPNGateway.ClientEnabled,
		"unknown peer at startup must NOT auto-wipe ClientEnabled")
	ts.Equal(unknownPeerID, tp.app.Conf.VPNGateway.GatewayPeerID,
		"unknown peer at startup must NOT auto-wipe GatewayPeerID")
}
