package metrics

import (
	"slices"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	SOCKS5ConnectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "socks5",
		Name:      "connections_total",
		Help:      "Total SOCKS5 connections.",
	}, []string{"direction"})

	SOCKS5ActiveConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "socks5",
		Name:      "active_connections",
		Help:      "Current active SOCKS5 connections.",
	}, []string{"direction"})

	// TODO: add?
	SOCKS5BytesTransferredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "socks5",
		Name:      "bytes_transferred_total",
		Help:      "Total bytes transferred via SOCKS5.",
	}, []string{"direction"})

	SOCKS5ErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "socks5",
		Name:      "errors_total",
		Help:      "Total SOCKS5 errors.",
	}, []string{"direction", "reason"})

	SOCKS5ConnectionDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "socks5",
		Name:      "connection_duration_seconds",
		Help:      "SOCKS5 connection duration in seconds.",
		Buckets:   append(slices.Clone(prometheus.DefBuckets), 20, 40, 80, 160, 320, 640, 1280),
	}, []string{"direction"})
)
