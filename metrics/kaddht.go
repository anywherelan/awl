package metrics

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// kadDHTNamespace prefixes every metric that comes out of the OpenTelemetry
// exporter. go-libp2p-kad-dht is the only OpenTelemetry metrics source in the
// build, and its own names (rpc.outbound.requests, ...) say nothing next to
// awl_* and libp2p_* in a shared registry.
const kadDHTNamespace = "libp2p_kad_dht"

var exportKadDHTMetrics = sync.OnceValue(func() error {
	exporter, err := otelprom.New(
		otelprom.WithRegisterer(prometheus.DefaultRegisterer),
		otelprom.WithNamespace(kadDHTNamespace),
		// The namespace already says where the metrics come from.
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		return fmt.Errorf("create prometheus exporter for opentelemetry: %w", err)
	}

	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)))

	return nil
})

// ExportKadDHTMetrics makes the RPC metrics of go-libp2p-kad-dht show up in the
// default Prometheus registry.
//
// kad-dht reports them to the global OpenTelemetry meter, which drops everything
// until a meter provider is installed. The provider is process-wide, so only the
// first call does the work; call it before the DHT starts to not lose the RPCs
// of its bootstrap.
func ExportKadDHTMetrics() error {
	return exportKadDHTMetrics()
}
