// Package metrics provides Prometheus metrics for AWL subsystems.
package metrics

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	namespace = "awl"
)

// P2pMetrics is an interface for getting p2p network stats used by the background updater.
type P2pMetrics interface {
	ConnectedPeersCount() int
	OpenConnectionsCount() int
	OpenStreamsCount() int64
	RoutingTableSize() int
	BootstrapPeersStats() (total int, connected int)
	IsConnected(peerID peer.ID) bool
	GetPeerLatency(id peer.ID) time.Duration
}

// ConfigMetrics is an interface for getting config data used by the background updater.
type ConfigMetrics interface {
	GetKnownPeersSnapshot() (total, confirmed, connected int, peerIDs []peer.ID)
	GetBlockedPeersCount() int
	GetAuthRequestCounts() (ingoing, outgoing int)
}

// StartBackgroundUpdater periodically updates gauge-type metrics from their data sources.
func StartBackgroundUpdater(ctx context.Context, conf ConfigMetrics, p2pMetrics P2pMetrics) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Initial update
	startTime := time.Now()
	NodeStartTimestamp.Set(float64(startTime.Unix()))
	updateGauges(conf, p2pMetrics, startTime)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updateGauges(conf, p2pMetrics, startTime)
		}
	}
}

func updateGauges(conf ConfigMetrics, p2pMetrics P2pMetrics, startTime time.Time) {
	// Peer metrics
	total, confirmed, connected, peerIDs := conf.GetKnownPeersSnapshot()
	PeersKnownTotal.Set(float64(total))
	PeersConfirmedTotal.Set(float64(confirmed))
	PeersConnected.Set(float64(connected))
	PeersBlockedTotal.Set(float64(conf.GetBlockedPeersCount()))

	ingoing, outgoing := conf.GetAuthRequestCounts()
	PeersAuthRequestsIngoing.Set(float64(ingoing))
	PeersAuthRequestsOutgoing.Set(float64(outgoing))

	// P2P metrics
	P2PConnectedPeers.Set(float64(p2pMetrics.ConnectedPeersCount()))
	P2POpenConnections.Set(float64(p2pMetrics.OpenConnectionsCount()))
	P2POpenStreams.Set(float64(p2pMetrics.OpenStreamsCount()))
	P2PDHTRoutingTableSize.Set(float64(p2pMetrics.RoutingTableSize()))

	bootstrapTotal, bootstrapConnected := p2pMetrics.BootstrapPeersStats()
	P2PBootstrapPeersTotal.Set(float64(bootstrapTotal))
	P2PBootstrapPeersConnected.Set(float64(bootstrapConnected))

	// Per-peer latency
	for _, peerID := range peerIDs {
		latency := p2pMetrics.GetPeerLatency(peerID)
		P2PPeerLatencySeconds.WithLabelValues(peerID.String()).Set(latency.Seconds())
	}

	// Node uptime
	NodeUptimeSeconds.Set(time.Since(startTime).Seconds())
}
