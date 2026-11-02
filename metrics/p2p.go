package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Read from the node on every scrape, see CollectFrom.
var (
	p2pBootstrapPeersTotalDesc = newDesc("p2p", "bootstrap_peers_total",
		"Total number of configured bootstrap peers.")

	p2pBootstrapPeersConnectedDesc = newDesc("p2p", "bootstrap_peers_connected",
		"Number of connected bootstrap peers.")

	p2pDHTRoutingTableSizeDesc = newDesc("p2p", "dht_routing_table_size",
		"DHT routing table size.")

	p2pOpenConnectionsDesc = newDesc("p2p", "open_connections",
		"Number of open connections.")

	p2pOpenStreamsDesc = newDesc("p2p", "open_streams",
		"Number of open streams.")

	p2pConnectedPeersDesc = newDesc("p2p", "connected_peers",
		"Total number of connected peers (all, not just known).")

	p2pPeerLatencySecondsDesc = newDesc("p2p", "peer_latency_seconds",
		"Latency to connected known peers in seconds, a moving average of pings. Absent until the first ping.",
		"peer_id")

	p2pBandwidthBytesTotalDesc = newDesc("p2p", "bandwidth_bytes_total",
		"Traffic of libp2p streams in bytes by protocol, without transport overhead. "+
			"Streams before their protocol is negotiated are counted as unknown.",
		"protocol", "direction")
)

var (
	P2PRedialsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "redials_total",
		Help: "Redials of a known peer right after it disconnected, by result: " +
			"our dial connected (success), it did not (failure), or the peer came back on its own first (peer_connected).",
	}, []string{"result"})

	P2PRedialDurationSeconds = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "redial_duration_seconds",
		Help:      "How long the dial of a redial took, by result.",
		Buckets:   []float64{0.25, 0.5, 1, 1.5, 2, 3, 4, 5},
	}, []string{"result"})

	P2PReconnectResetsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "reconnect_resets_total",
		Help: "Times the reconnection schedule was reset because the network came back, by reason: " +
			"bootstrap peers became reachable again (bootstrap_returned) or the application said so (notified).",
	}, []string{"reason"})
)
