package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	P2PBootstrapPeersTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "bootstrap_peers_total",
		Help:      "Total number of configured bootstrap peers.",
	})

	P2PBootstrapPeersConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "bootstrap_peers_connected",
		Help:      "Number of connected bootstrap peers.",
	})

	P2PDHTRoutingTableSize = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "dht_routing_table_size",
		Help:      "DHT routing table size.",
	})

	P2POpenConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "open_connections",
		Help:      "Number of open connections.",
	})

	P2POpenStreams = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "open_streams",
		Help:      "Number of open streams.",
	})

	P2PConnectedPeers = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "connected_peers",
		Help:      "Total number of connected peers (all, not just known).",
	})

	P2PPeerLatencySeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "p2p",
		Name:      "peer_latency_seconds",
		Help:      "Last measured latency to known peers in seconds.",
	}, []string{"peer_id"})
)
