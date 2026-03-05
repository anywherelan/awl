package metrics

import (
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	NodeInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "node",
		Name:      "info",
		Help:      "Static node information.",
	}, []string{"version", "peer_id", "go_version", "os", "arch"})

	NodeUptimeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "node",
		Name:      "uptime_seconds",
		Help:      "Node uptime in seconds.",
	})

	NodeStartTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "node",
		Name:      "start_timestamp",
		Help:      "Unix timestamp of node start.",
	})
)

// SetNodeInfo sets the static node info gauge and the start timestamp.
func SetNodeInfo(version, peerID string) {
	NodeInfo.WithLabelValues(version, peerID, runtime.Version(), runtime.GOOS, runtime.GOARCH).Set(1)
	NodeStartTimestamp.Set(float64(time.Now().Unix()))
}
