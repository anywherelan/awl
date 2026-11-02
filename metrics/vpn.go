package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	VPNPacketsSentTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "packets_sent_total",
		Help:      "Total VPN packets written to tunnel streams.",
	})

	VPNPacketsReceivedTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "packets_received_total",
		Help:      "Total VPN packets received from tunnel streams.",
	})

	VPNBytesSentTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "bytes_sent_total",
		Help:      "Total VPN bytes sent via tunnel streams.",
	})

	VPNBytesReceivedTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "bytes_received_total",
		Help:      "Total VPN bytes received from tunnel streams.",
	})

	VPNPacketsDroppedTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "packets_dropped_total",
		Help:      "Total VPN packets dropped.",
	}, []string{"reason"})

	VPNTunReadErrorsTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "tun_read_errors_total",
		Help:      "Total TUN device read errors.",
	})

	VPNTunWriteErrorsTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "tun_write_errors_total",
		Help:      "Total TUN device write errors.",
	})

	VPNStreamOpenErrorsTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vpn",
		Name:      "stream_open_errors_total",
		Help:      "Total errors opening tunnel streams.",
	})
)
