// Package metrics provides Prometheus metrics for AWL subsystems.
package metrics

import (
	"runtime"
	"sync/atomic"
	"time"

	libp2pmetrics "github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	namespace = "awl"
)

// Registry holds the awl metrics, the awl /metrics endpoint serves it along
// with the default registry. They are kept out of the default registry because
// awl-bootstrap-node imports awl packages that use them: it serves the default
// registry and would export the awl metrics too, all of them zero.
var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

// P2pMetrics is the part of p2p.P2p the node gauges are read from.
type P2pMetrics interface {
	ConnectedPeersCount() int
	OpenConnectionsCount() int
	OpenStreamsCount() int64
	RoutingTableSize() int
	BootstrapPeersStats() (total int, connected int)
	IsConnected(peerID peer.ID) bool
	HasDirectConnection(peerID peer.ID) bool
	GetPeerLatency(id peer.ID) time.Duration
	NetworkStatsByProtocol() map[protocol.ID]libp2pmetrics.Stats
}

// ConfigMetrics is the part of the config and the auth state the node gauges
// are read from.
type ConfigMetrics interface {
	GetKnownPeersSnapshot() (total, confirmed int, peerIDs []peer.ID)
	GetBlockedPeersCount() int
	GetAuthRequestCounts() (ingoing, outgoing int)
}

// NodeInfo is what awl_node_info tells about the node.
type NodeInfo struct {
	Version string
	PeerID  string
	// App is the application the node runs in: awl, awl-tray or android.
	App string
}

type source struct {
	node      NodeInfo
	conf      ConfigMetrics
	p2p       P2pMetrics
	startTime time.Time
}

// scrapeSource is what collectNode reads on every scrape.
var scrapeSource atomic.Pointer[source]

func init() {
	// CollectorFunc describes itself by collecting, and nothing is collected
	// before CollectFrom: the registry takes it as an unchecked collector.
	Registry.MustRegister(prometheus.CollectorFunc(collectNode))
}

// CollectFrom makes the node gauges read their values from conf and p2pMetrics
// on every scrape: they are always fresh, and the series of a peer that is
// gone disappear by themselves. The gauges are absent until it is called. A
// node restarted in the same process (awl-tray, Android) calls it again, and
// the new sources replace the old ones.
func CollectFrom(node NodeInfo, conf ConfigMetrics, p2pMetrics P2pMetrics) {
	scrapeSource.Store(&source{node: node, conf: conf, p2p: p2pMetrics, startTime: time.Now()})
}

// StopCollecting drops the sources set by CollectFrom, so a closed node is not
// kept in memory. The gauges are absent again.
func StopCollecting() {
	scrapeSource.Store(nil)
}

func collectNode(ch chan<- prometheus.Metric) {
	s := scrapeSource.Load()
	if s == nil {
		return
	}
	gauge := func(desc *prometheus.Desc, value float64, labelValues ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, labelValues...)
	}

	// Node
	gauge(nodeInfoDesc, 1, s.node.Version, s.node.PeerID, runtime.Version(), runtime.GOOS, runtime.GOARCH, s.node.App)
	gauge(nodeStartTimestampDesc, float64(s.startTime.Unix()))
	gauge(nodeUptimeSecondsDesc, time.Since(s.startTime).Seconds())

	// Peer metrics
	total, confirmed, peerIDs := s.conf.GetKnownPeersSnapshot()
	direct, relayed := 0, 0
	for _, peerID := range peerIDs {
		if !s.p2p.IsConnected(peerID) {
			continue
		}
		if s.p2p.HasDirectConnection(peerID) {
			direct++
		} else {
			relayed++
		}
		if latency := s.p2p.GetPeerLatency(peerID); latency > 0 {
			gauge(p2pPeerLatencySecondsDesc, latency.Seconds(), peerID.String())
		}
	}
	gauge(peersKnownTotalDesc, float64(total))
	gauge(peersConfirmedTotalDesc, float64(confirmed))
	gauge(peersConnectedDesc, float64(direct), pathDirect)
	gauge(peersConnectedDesc, float64(relayed), pathRelay)
	gauge(peersBlockedTotalDesc, float64(s.conf.GetBlockedPeersCount()))

	ingoing, outgoing := s.conf.GetAuthRequestCounts()
	gauge(peersAuthRequestsIngoingDesc, float64(ingoing))
	gauge(peersAuthRequestsOutgoingDesc, float64(outgoing))

	// P2P metrics
	gauge(p2pConnectedPeersDesc, float64(s.p2p.ConnectedPeersCount()))
	gauge(p2pOpenConnectionsDesc, float64(s.p2p.OpenConnectionsCount()))
	gauge(p2pOpenStreamsDesc, float64(s.p2p.OpenStreamsCount()))
	gauge(p2pDHTRoutingTableSizeDesc, float64(s.p2p.RoutingTableSize()))

	bootstrapTotal, bootstrapConnected := s.p2p.BootstrapPeersStats()
	gauge(p2pBootstrapPeersTotalDesc, float64(bootstrapTotal))
	gauge(p2pBootstrapPeersConnectedDesc, float64(bootstrapConnected))

	// The libp2p bandwidth counter is never reset or trimmed, so its totals
	// only grow and are counters
	for proto, stats := range s.p2p.NetworkStatsByProtocol() {
		// An empty label value is the same as no label
		label := string(proto)
		if label == "" {
			label = "unknown"
		}
		ch <- prometheus.MustNewConstMetric(p2pBandwidthBytesTotalDesc, prometheus.CounterValue, float64(stats.TotalIn), label, "in")
		ch <- prometheus.MustNewConstMetric(p2pBandwidthBytesTotalDesc, prometheus.CounterValue, float64(stats.TotalOut), label, "out")
	}
}

func newDesc(subsystem, name, help string, labelNames ...string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, name), help, labelNames, nil)
}
