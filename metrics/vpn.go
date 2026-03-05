package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	VPNPacketsSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "packets_sent_total",
		Help:      "Total VPN packets written to tunnel streams.",
	})

	VPNPacketsReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "packets_received_total",
		Help:      "Total VPN packets received from tunnel streams.",
	})

	VPNBytesSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "bytes_sent_total",
		Help:      "Total VPN bytes sent via tunnel streams.",
	})

	VPNBytesReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "bytes_received_total",
		Help:      "Total VPN bytes received from tunnel streams.",
	})

	VPNPacketsDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "packets_dropped_total",
		Help:      "Total VPN packets dropped.",
	}, []string{"reason"})

	VPNTunReadErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "tun_read_errors_total",
		Help:      "Total TUN device read errors.",
	})

	VPNTunWriteErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "tun_write_errors_total",
		Help:      "Total TUN device write errors.",
	})

	VPNStreamOpenErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "stream_open_errors_total",
		Help:      "Total errors opening tunnel streams.",
	})
)
