package awl

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	simlibp2p "github.com/libp2p/go-libp2p/x/simlibp2p"
	"github.com/marcopolo/simnet"
	"github.com/multiformats/go-multiaddr"
	"github.com/olekukonko/tablewriter"
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

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Scenario", "Latency", "Bandwidth Limit", "Actual Throughput", "Utilization", "Packet Loss"})

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ts := NewSimnetTestSuite(t)

			net := &simnet.Simnet{}
			net.LatencyFunc = simnet.StaticLatency(sc.latency)
			net.Start()
			defer net.Close()

			// TODO: try with different packet sizes
			//  we probably should aim for one packet per UDP datagram
			const packetSize = 3500 // Typical VPN packet size
			const testDuration = 30 * time.Second

			// Setup link properties for the simulation
			// We simulate a symmetric link between the two peers
			linkSettings := simnet.NodeBiDiLinkSettings{
				Downlink: simnet.LinkSettings{BitsPerSecond: sc.bandwidthMbps},
				Uplink:   simnet.LinkSettings{BitsPerSecond: sc.bandwidthMbps},
			}

			// Create two peers
			// uncomment to debug QUIC events
			// t.Setenv("QLOGDIR", "./test-simnet")
			ctx := t.Context()
			extraLibp2pOpts := []libp2p.Option{
				simlibp2p.QUICSimnet(net, linkSettings),
			}

			listenAddrs1 := []multiaddr.Multiaddr{
				multiaddr.StringCast("/ip4/1.2.3.1/udp/1234/quic-v1"),
			}
			peer1 := ts.newTestPeer(true, listenAddrs1, extraLibp2pOpts)
			listenAddrs2 := []multiaddr.Multiaddr{
				multiaddr.StringCast("/ip4/1.2.3.2/udp/1234/quic-v1"),
			}
			peer2 := ts.newTestPeer(true, listenAddrs2, extraLibp2pOpts)
			ts.makeFriendsSimnet(peer1, peer2)

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
