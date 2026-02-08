package awl

import (
	"bytes"
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

	if os.Getenv("CI") != "" {
		t.Skip("skip in CI because it's a benchmark")
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
			peer2.tun.ReferenceInboundPacketLen = packetSize
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

	if os.Getenv("CI") != "" {
		t.Skip("skip in CI because it's a benchmark")
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
