package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	DNSQueriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "dns",
		Name:      "queries_total",
		Help:      "Total DNS queries handled.",
	}, []string{"handler"})

	DNSQueryErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "dns",
		Name:      "query_errors_total",
		Help:      "Total DNS query errors (upstream failures).",
	})

	DNSQueryDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "dns",
		Name:      "query_duration_seconds",
		Help:      "DNS query duration in seconds.",
		Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 10},
	})
)
