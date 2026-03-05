package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	PeersKnownTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "known_total",
		Help:      "Total number of known peers.",
	})

	PeersConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "connected",
		Help:      "Number of currently connected known peers.",
	})

	PeersConfirmedTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "confirmed_total",
		Help:      "Number of confirmed (mutual) peers.",
	})

	PeersBlockedTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "blocked_total",
		Help:      "Number of blocked peers.",
	})

	PeersAuthRequestsIngoing = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "auth_requests_ingoing",
		Help:      "Number of pending ingoing auth requests.",
	})

	PeersAuthRequestsOutgoing = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "auth_requests_outgoing",
		Help:      "Number of pending outgoing auth requests.",
	})

	PeersAuthRequestsSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "auth_requests_sent_total",
		Help:      "Total number of auth requests sent.",
	})

	PeersAuthRequestsReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "auth_requests_received_total",
		Help:      "Total number of auth requests received.",
	})

	PeersStatusRequestsSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "status_requests_sent_total",
		Help:      "Total number of status requests sent.",
	})

	PeersStatusRequestsReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "status_requests_received_total",
		Help:      "Total number of status requests received.",
	})

	PeersConnectionEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "connection_events_total",
		Help:      "Total peer connection events.",
	}, []string{"event"})
)
