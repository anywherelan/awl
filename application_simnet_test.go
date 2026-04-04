package awl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/olekukonko/tablewriter"
	"golang.org/x/net/proxy"

	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/vpn"
)

const EnvRunPerfTests = "AWL_PERF_TESTS"

/*
TestSimulatedTunnelPerformance performs a benchmark of the AWL VPN tunnel under simulated network conditions.

It uses:
1. simlibp2p (github.com/libp2p/go-libp2p/x/simlibp2p): A simulated network transport for libp2p.
   This allows us to run libp2p hosts that communicate over a simulated network rather than
   actual OS sockets. This is faster and more deterministic.

2. simnet (github.com/marcopolo/simnet): A network simulator that simlibp2p uses underneath.
   Simnet allows defining network topologies with specific link properties like latency and bandwidth_mbps.
   This enables testing how the VPN protocol behaves under "Fiber", "Satellite", or "DSL" conditions.
*/

func TestSimulatedTunnelPerformance(t *testing.T) {
	const Mbps = 1_000_000

	if os.Getenv(EnvRunPerfTests) == "" {
		t.Skipf("skip perf test because %s env is empty", EnvRunPerfTests)
	}

	scenarios := []struct {
		name          string
		latency       time.Duration
		bandwidthMbps int
	}{
		{
			name:          "Fiber_200Mbps_1ms",
			latency:       1 * time.Millisecond,
			bandwidthMbps: 200 * Mbps,
		},
		{
			name:          "LongDistFiber_200Mbps_200ms",
			latency:       200 * time.Millisecond,
			bandwidthMbps: 200 * Mbps,
		},
		{
			name:          "Fiber_100Mbps_1ms",
			latency:       1 * time.Millisecond,
			bandwidthMbps: 100 * Mbps,
		},
		{
			name:          "LongDistFiber_100Mbps_200ms",
			latency:       200 * time.Millisecond,
			bandwidthMbps: 100 * Mbps,
		},

		{
			name:          "Cable_10Mbps_1ms",
			latency:       1 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},
		{
			name:          "Cable_10Mbps_10ms",
			latency:       10 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},
		{
			name:          "Cable_10Mbps_100ms",
			latency:       100 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},
		{
			name:          "Cable_10Mbps_200ms",
			latency:       200 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},
		{
			name:          "LongDistCable_10Mbps_300ms",
			latency:       300 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},

		{
			name:          "Cable_50Mbps_1ms",
			latency:       1 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},
		{
			name:          "Cable_50Mbps_10ms",
			latency:       10 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},
		{
			name:          "Cable_50Mbps_100ms",
			latency:       100 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},
		{
			name:          "Cable_50Mbps_200ms",
			latency:       200 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},
		{
			name:          "LongDistCable_50Mbps_300ms",
			latency:       300 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},

		{
			name:          "DSL_20Mbps_25ms",
			latency:       25 * time.Millisecond,
			bandwidthMbps: 20 * Mbps,
		},
		{
			name:          "LTE_30Mbps_40ms",
			latency:       40 * time.Millisecond,
			bandwidthMbps: 30 * Mbps,
		},
	}

	// TODO: try with different packet sizes
	//  we probably should aim for one packet per UDP datagram
	const packetSize = vpn.InterfaceMTU
	const testDuration = 30 * time.Second

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Scenario", "Latency", "Bandwidth Limit", "Actual Throughput", "Utilization", "Packet Loss"})

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ts := NewSimnetTestSuite(t)

			// Create two peers connected over simulated network
			// uncomment to debug QUIC events
			// t.Setenv("QLOGDIR", "./test-simnet")
			ctx := t.Context()
			peer1, peer2 := ts.NewSimnetPeerPair(sc.latency, sc.bandwidthMbps, nil, nil)

			packet := testPacket(packetSize)
			peer2.tun.SetInboundCapture(packetSize, nil)
			peer2.tun.ClearInboundCount()

			// Send packets
			done := make(chan struct{})
			startTime := time.Now()

			packetsBatch := make([][]byte, TestTUNBatchSize*10)
			for i := range packetsBatch {
				packetsBatch[i] = packet
			}

			go func() {
				defer close(done)
				timer := time.NewTimer(testDuration)
				defer timer.Stop()

				for i := 0; ; i++ {
					select {
					case <-timer.C:
						return
					case <-ctx.Done():
						return
					case peer1.tun.Outbound <- packetsBatch:
						// ok
					}

					// TODO: add ratelimit for TestTUN to remove busyloop
				}
			}()

			// Wait for sender to finish
			<-done
			duration := time.Since(startTime)

			// Allow some time for packets to arrive
			time.Sleep(sc.latency * 2)

			// Collect metrics
			received := peer2.tun.InboundCount()
			sent := peer1.tun.OutboundCount()

			packetLoss := (float64(1) - float64(received)/float64(sent)) * 100

			totalBits := float64(received) * float64(packetSize) * 8
			actualMbps := (totalBits / duration.Seconds()) / Mbps
			expectedMbps := sc.bandwidthMbps / Mbps

			utilization := (actualMbps / float64(expectedMbps)) * 100

			table.Append([]string{
				sc.name,
				sc.latency.String(),
				fmt.Sprintf("%d Mbps", expectedMbps),
				fmt.Sprintf("%.2f Mbps", actualMbps),
				fmt.Sprintf("%.2f %%", utilization),
				fmt.Sprintf("%.2f %%", packetLoss),
				// TODO: calculate p50/p95/p99 latency, jitter
			})
		})

		// cool down a bit
		time.Sleep(time.Second)
	}

	table.Render()
}

/*
TestSimulatedSOCKS5ProxyPerformance benchmarks SOCKS5 proxy performance under simulated network conditions.

It uses:
1. simlibp2p for simulated QUIC transport between peers
2. simnet for configurable network latency and bandwidth
3. Real SOCKS5 client/server for actual proxy connections
4. Real HTTP client/server for end-to-end measurements

The data flow is:
HTTP Client → SOCKS5 Listener (real TCP) → p2p Stream (simnet QUIC) → SOCKS5 Server → HTTP Server (real TCP)
*/
func TestSimulatedSOCKS5ProxyPerformance(t *testing.T) {
	// TODO: we have test for socks5 client receiving. Add test for socks5 client sending

	const Mbps = 1_000_000

	if os.Getenv(EnvRunPerfTests) == "" {
		t.Skipf("skip perf test because %s env is empty", EnvRunPerfTests)
	}

	scenarios := []struct {
		name          string
		latency       time.Duration
		bandwidthMbps int
	}{
		{
			name:          "WARM-UP",
			latency:       10 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},
		{
			name:          "Fiber_100Mbps_1ms",
			latency:       1 * time.Millisecond,
			bandwidthMbps: 100 * Mbps,
		},
		{
			name:          "LongDistFiber_100Mbps_200ms",
			latency:       200 * time.Millisecond,
			bandwidthMbps: 100 * Mbps,
		},

		{
			name:          "Cable_50Mbps_10ms",
			latency:       10 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},
		{
			name:          "LongDistCable_50Mbps_300ms",
			latency:       300 * time.Millisecond,
			bandwidthMbps: 50 * Mbps,
		},

		{
			name:          "Cable_10Mbps_100ms",
			latency:       100 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},
		{
			name:          "LongDistCable_10Mbps_300ms",
			latency:       300 * time.Millisecond,
			bandwidthMbps: 10 * Mbps,
		},
	}

	const testDuration = 20 * time.Second
	const testLastDuration = 5 * time.Second

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Scenario", "Latency", "Bandwidth Limit", "Throughput\navg", "Throughput\nlast 5 sec", "Utilization", "TTFB"})

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ts := NewSimnetTestSuite(t)

			ctx := t.Context()

			// Create two peers connected over simulated network
			// peer1: SOCKS5 client side (listener enabled)
			// peer2: SOCKS5 server side (proxying enabled)
			peer1, peer2 := ts.NewSimnetPeerPair(sc.latency, sc.bandwidthMbps,
				&SOCKS5PeerConfig{ListenerEnabled: true, ProxyingEnabled: false},
				&SOCKS5PeerConfig{ListenerEnabled: false, ProxyingEnabled: true},
			)

			// Configure peer2 to allow peer1 to use as exit node
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

			// Wait for status exchange to propagate AllowedUsingAsExitNode to peer1
			ts.Eventually(func() bool {
				peer2Config, err := peer1.api.KnownPeerConfig(peer2.PeerID())
				ts.NoError(err)
				return peer2Config.AllowedUsingAsExitNode
			}, 2*time.Second, 100*time.Millisecond)

			// Set peer2 as proxy for peer1
			peer1.app.SOCKS5.SetProxyPeerID(peer2.PeerID())
			peer2.app.SOCKS5.SetProxyingLocalhostEnabled(true)

			// Setup raw TCP server that sends unlimited data
			tcpAddr := startUnlimitedTCPServer(t)

			dialer, err := proxy.SOCKS5("tcp", peer1.app.Conf.SOCKS5.ListenAddress, nil, nil)
			ts.NoError(err)

			// Measure TTFB and throughput
			connectStart := time.Now()

			// Connect through SOCKS5 proxy to TCP server
			conn, err := dialer.Dial("tcp", tcpAddr)
			ts.NoError(err)
			defer conn.Close()

			// Read first byte to measure TTFB
			firstByte := make([]byte, 1)
			_, err = io.ReadFull(conn, firstByte)
			ts.NoError(err)
			ttfb := time.Since(connectStart)

			// Measure throughput for testDuration
			const bufSize = 1 << 20
			buf := make([]byte, bufSize)

			totalBytes := int64(1)
			startTime := time.Now()

			startTimeLastSeconds := time.Time{}
			bytesLastSeconds := int64(0)

			for time.Since(startTime) < testDuration || ctx.Err() != nil {
				if startTimeLastSeconds.IsZero() && time.Since(startTime) > testDuration-testLastDuration {
					startTimeLastSeconds = time.Now()
				}

				n, err := conn.Read(buf)
				totalBytes += int64(n)
				if !startTimeLastSeconds.IsZero() {
					bytesLastSeconds += int64(n)
				}
				if err != nil {
					t.Errorf("Read error after %d bytes: %v", totalBytes, err)
					break
				}
			}

			duration := time.Since(startTime)
			durationLast := time.Since(startTimeLastSeconds)

			// Calculate metrics
			throughputMbps := (float64(totalBytes) * 8) / duration.Seconds() / float64(Mbps)
			throughputMbpsLastSeconds := (float64(bytesLastSeconds) * 8) / durationLast.Seconds() / float64(Mbps)
			expectedMbps := float64(sc.bandwidthMbps) / float64(Mbps)
			utilization := (throughputMbps / expectedMbps) * 100

			table.Append([]string{
				sc.name,
				sc.latency.String(),
				fmt.Sprintf("%d Mbps", sc.bandwidthMbps/Mbps),
				fmt.Sprintf("%.2f Mbps", throughputMbps),
				fmt.Sprintf("%.2f Mbps", throughputMbpsLastSeconds),
				fmt.Sprintf("%.2f %%", utilization),
				ttfb.Round(100 * time.Microsecond).String(),
			})
		})

		// Cool down between tests
		time.Sleep(time.Second)
	}

	table.Render()
}

type gatewayPerfScenario struct {
	name         string
	latency      time.Duration
	bandwidthBps int
}

var gatewayPerfScenarios = []gatewayPerfScenario{
	{"WARM-UP", 10 * time.Millisecond, 50_000_000},
	{"Fiber_100Mbps_1ms", 1 * time.Millisecond, 100_000_000},
	{"Cable_50Mbps_10ms", 10 * time.Millisecond, 50_000_000},
	{"LongDistCable_50Mbps_300ms", 300 * time.Millisecond, 50_000_000},
	{"Cable_10Mbps_100ms", 100 * time.Millisecond, 10_000_000},
	{"LongDistCable_10Mbps_300ms", 300 * time.Millisecond, 10_000_000},
}

// setupSimnetGatewayPair builds a simnet peer pair where the client uses the
// exit node as a VPN gateway. Cannot reuse setupGatewayPeers because that one
// goes through the DHT, which simnet does not have.
//
// Steps:
//  1. Pair them with NewSimnetPeerPair (calls makeFriendsSimnet under the hood).
//  2. Exit node advertises VPN gateway service via SetServeAsVPNGateway —
//     persisted in config so the next outgoing PeerStatusInfo carries the flag.
//  3. UpdatePeerSettings on the exit node grants the client exit-node
//     permission AND triggers an immediate ExchangeNewStatusInfo. That single
//     bidirectional exchange propagates both AllowedUsingAsExitNode and
//     RemoteServesAsVPNGateway to the client in one round-trip.
//  4. Wait until the client's KnownPeer.CanUseAsVPNGateway() returns true,
//     since that is what SetGatewayPeer validates against.
func (ts *TestSuite) setupSimnetGatewayPair(latency time.Duration, bandwidthBps int) (client, exitNode TestPeer) {
	client, exitNode = ts.NewSimnetPeerPair(latency, bandwidthBps, nil, nil)

	exitNode.app.Tunnel.SetVPNGatewayServerEnabled(true)

	clientCfgOnExit, err := exitNode.api.KnownPeerConfig(client.PeerID())
	ts.NoError(err)
	err = exitNode.api.UpdatePeerSettings(entity.UpdatePeerSettingsRequest{
		PeerID:               client.PeerID(),
		Alias:                clientCfgOnExit.Alias,
		DomainName:           clientCfgOnExit.DomainName,
		IPAddr:               clientCfgOnExit.IPAddr,
		AllowUsingAsExitNode: true,
	})
	ts.NoError(err)

	ts.Eventually(func() bool {
		kp, ok := client.app.Conf.GetPeer(exitNode.PeerID())
		return ok && kp.CanUseAsVPNGateway()
	}, 5*time.Second, 50*time.Millisecond)

	ts.NoError(client.app.Tunnel.SetVPNGatewayPeer(exitNode.app.P2p.PeerID()))
	return client, exitNode
}

// runGatewayBlast injects gateway-mode packets (src=10.66.0.1, dst=8.8.8.8) at
// `sender` for the given duration, then sleeps for `drainTail` to let in-flight
// packets reach their final TUN. The caller chooses which peer's InboundCount
// to read (one-way: exit node; round-trip: client).
func runGatewayBlast(ctx context.Context, sender TestPeer, packetSize int, duration, drainTail time.Duration) (actualDuration time.Duration) {
	packet := testPacketWithSrcDest(packetSize, "10.66.0.1", "8.8.8.8")
	packetsBatch := make([][]byte, TestTUNBatchSize*10)
	for i := range packetsBatch {
		packetsBatch[i] = packet
	}

	startTime := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(duration)
		defer timer.Stop()

		for {
			select {
			case <-timer.C:
				return
			case <-ctx.Done():
				return
			case sender.tun.Outbound <- packetsBatch:
				// ok
			}
		}
	}()

	<-done
	actualDuration = time.Since(startTime)
	time.Sleep(drainTail)
	return
}

// startGatewayKernelReflector simulates the exit node's kernel: every packet
// landing on the exit node's TUN (Forward-tagged from the client and
// src-rewritten by writeInboundBatch to clientAssignedIP, with dst=8.8.8.8)
// is captured, src↔dst swapped, checksum recomputed, and re-injected on
// exitNode.tun.Outbound — mimicking a reply from the internet that conntrack
// has rewritten back to dst=client.
//
// testTun.Write uses a non-blocking send for the capture channel; under
// saturation some packets may be dropped at that boundary. Such drops show up
// as elevated packet loss on the client side, which is exactly what the
// round-trip test reports.
func startGatewayKernelReflector(exitNode TestPeer, packetSize int) (stop func()) {
	const captureChanSize = 8192
	captureCh := make(chan []byte, captureChanSize)
	exitNode.tun.SetInboundCapture(packetSize, captureCh)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case raw, ok := <-captureCh:
				if !ok {
					return
				}
				batch := make([][]byte, 0, TestTUNBatchSize)
				batch = append(batch, swapAndRecalc(raw))
			drain:
				for len(batch) < TestTUNBatchSize {
					select {
					case more, ok := <-captureCh:
						if !ok {
							break drain
						}
						batch = append(batch, swapAndRecalc(more))
					default:
						break drain
					}
				}
				select {
				case exitNode.tun.Outbound <- batch:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// swapAndRecalc returns a copy of raw with IPv4 src and dst swapped and the
// header checksum recomputed. The body is left untouched.
func swapAndRecalc(raw []byte) []byte {
	out := make([]byte, len(raw))
	copy(out, raw)
	p := vpn.Packet{Packet: out}
	if !p.Parse() {
		return out
	}
	srcCopy := append([]byte{}, p.Src...)
	copy(p.Src, p.Dst)
	copy(p.Dst, srcCopy)
	p.RecalculateChecksum()
	return out
}

// appendGatewayPerfRow renders one scenario's results into the given table.
// `received` is the inbound packet count at the chosen receive end (exit node
// for one-way, client for round-trip); `sent` is the client's TUN outbound
// count.
func appendGatewayPerfRow(table *tablewriter.Table, sc gatewayPerfScenario, received, sent int64, packetSize int, duration time.Duration) {
	const Mbps = 1_000_000
	var packetLoss float64
	if sent > 0 {
		packetLoss = (1 - float64(received)/float64(sent)) * 100
	}
	totalBits := float64(received) * float64(packetSize) * 8
	actualMbps := (totalBits / duration.Seconds()) / Mbps
	expectedMbps := sc.bandwidthBps / Mbps
	var utilization float64
	if expectedMbps > 0 {
		utilization = (actualMbps / float64(expectedMbps)) * 100
	}
	table.Append([]string{
		sc.name,
		sc.latency.String(),
		fmt.Sprintf("%d Mbps", expectedMbps),
		fmt.Sprintf("%.2f Mbps", actualMbps),
		fmt.Sprintf("%.2f %%", utilization),
		fmt.Sprintf("%.2f %%", packetLoss),
	})
}

/*
TestSimulatedGatewayPerformance benchmarks VPN-gateway client→exit-node throughput
under simulated network conditions.

Compared to TestSimulatedTunnelPerformance the only difference is the receive-side
write path: the client stamps GatewayDirForward in the on-wire length-prefix,
the exit node reads the tag and applies a per-packet src-only rewrite
(preserving the real internet destination) instead of the full src/dst rewrite
used for normal awl peer-to-peer traffic. Both paths share the same single
tun.Write per batch. Run both tests together to compare the two paths.
*/
func TestSimulatedGatewayPerformance(t *testing.T) {
	if os.Getenv(EnvRunPerfTests) == "" {
		t.Skipf("skip perf test because %s env is empty", EnvRunPerfTests)
	}

	const packetSize = vpn.InterfaceMTU
	const testDuration = 30 * time.Second

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Scenario", "Latency", "Bandwidth Limit", "Actual Throughput", "Utilization", "Packet Loss"})

	for _, sc := range gatewayPerfScenarios {
		t.Run(sc.name, func(t *testing.T) {
			ts := NewSimnetTestSuite(t)
			ctx := t.Context()

			client, exitNode := ts.setupSimnetGatewayPair(sc.latency, sc.bandwidthBps)

			exitNode.tun.SetInboundCapture(packetSize, nil)
			exitNode.tun.ClearInboundCount()

			duration := runGatewayBlast(ctx, client, packetSize, testDuration, sc.latency*2)

			appendGatewayPerfRow(table, sc,
				exitNode.tun.InboundCount(),
				client.tun.OutboundCount(),
				packetSize, duration)
		})
		time.Sleep(time.Second)
	}

	table.Render()
}

/*
TestSimulatedGatewayRoundTripPerformance benchmarks the full bidirectional
gateway path: client → libp2p tunnel → exit node TUN → simulated kernel
reflector → exit node TUN → libp2p tunnel → client TUN.

The reflector swaps src↔dst on each packet that reaches the exit node's TUN
and re-injects it as if it were a reply from the internet (i.e. it stands in
for ip_forward + MASQUERADE + conntrack). Throughput is measured at the
client's TUN inbound count, so the reported number is end-to-end success rate
of the data round-trip.

Cross-reference with TestSimulatedGatewayPerformance (the one-way version) to
see how much of the throughput cost comes from the return path vs. the
forward path alone.
*/
func TestSimulatedGatewayRoundTripPerformance(t *testing.T) {
	if os.Getenv(EnvRunPerfTests) == "" {
		t.Skipf("skip perf test because %s env is empty", EnvRunPerfTests)
	}

	const packetSize = vpn.InterfaceMTU
	const testDuration = 30 * time.Second

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Scenario", "Latency", "Bandwidth Limit", "Round-Trip Throughput", "Utilization", "Packet Loss"})

	for _, sc := range gatewayPerfScenarios {
		t.Run(sc.name, func(t *testing.T) {
			ts := NewSimnetTestSuite(t)
			ctx := t.Context()

			client, exitNode := ts.setupSimnetGatewayPair(sc.latency, sc.bandwidthBps)

			stop := startGatewayKernelReflector(exitNode, packetSize)
			defer stop()

			client.tun.SetInboundCapture(packetSize, nil)
			client.tun.ClearInboundCount()

			// Drain tail is 4× latency to cover the full round-trip plus
			// libp2p stream-flush time after the blast goroutine stops.
			duration := runGatewayBlast(ctx, client, packetSize, testDuration, sc.latency*4)

			appendGatewayPerfRow(table, sc,
				client.tun.InboundCount(),
				client.tun.OutboundCount(),
				packetSize, duration)
		})
		time.Sleep(time.Second)
	}

	table.Render()
}

// startUnlimitedTCPServer starts a TCP server that sends unlimited data to any client.
// Returns the server address. Server is automatically closed when test ends.
func startUnlimitedTCPServer(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start TCP server: %v", err)
	}

	t.Cleanup(func() {
		listener.Close()
	})

	const chunkSize = 1 << 20 // 1 MB
	chunk := bytes.Repeat([]byte("X"), chunkSize)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // Listener closed
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					_, err2 := c.Write(chunk)
					if err2 != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return listener.Addr().String()
}
