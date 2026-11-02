package metrics

import (
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	libp2pmetrics "github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestCollectNode_NoSource(t *testing.T) {
	// what awl-bootstrap-node gets: it never calls CollectFrom
	require.Empty(t, collect(t))
}

func TestCollectNode_Stopped(t *testing.T) {
	// a node closed in awl-tray or Android
	CollectFrom(NodeInfo{}, fakeConfig{}, fakeP2p{})
	StopCollecting()
	require.Empty(t, collect(t))
}

func TestCollectNode(t *testing.T) {
	direct, relayed, offline := peer.ID("direct"), peer.ID("relayed"), peer.ID("offline")
	CollectFrom(
		NodeInfo{Version: "v1.2.3", PeerID: "self", App: "awl-tray"},
		fakeConfig{total: 3, confirmed: 2, peerIDs: []peer.ID{direct, relayed, offline}},
		fakeP2p{
			// value: whether one of the connections is direct
			connected: map[peer.ID]bool{direct: true, relayed: false},
			latency: map[peer.ID]time.Duration{
				direct: 20 * time.Millisecond,
				// relayed is not pinged yet
				offline: 50 * time.Millisecond,
			},
			bandwidth: map[protocol.ID]libp2pmetrics.Stats{
				"/awl/kad/1.0.0": {TotalIn: 100, TotalOut: 200},
				"":               {TotalIn: 5, TotalOut: 6},
			},
		},
	)
	t.Cleanup(StopCollecting)

	values := collect(t)

	nodeInfo := fmt.Sprintf(`awl_node_info{app="awl-tray",arch=%q,go_version=%q,os=%q,peer_id="self",version="v1.2.3"}`,
		runtime.GOARCH, runtime.Version(), runtime.GOOS)
	require.Equal(t, map[string]float64{
		nodeInfo:                                                          1,
		`awl_peers_known_total`:                                           3,
		`awl_peers_confirmed_total`:                                       2,
		`awl_peers_connected{path="direct"}`:                              1,
		`awl_peers_connected{path="relay"}`:                               1,
		`awl_peers_blocked_total`:                                         1,
		`awl_peers_auth_requests_ingoing`:                                 4,
		`awl_peers_auth_requests_outgoing`:                                5,
		`awl_p2p_connected_peers`:                                         7,
		`awl_p2p_open_connections`:                                        8,
		`awl_p2p_open_streams`:                                            9,
		`awl_p2p_dht_routing_table_size`:                                  10,
		`awl_p2p_bootstrap_peers_total`:                                   4,
		`awl_p2p_bootstrap_peers_connected`:                               3,
		`awl_p2p_peer_latency_seconds{peer_id="` + direct.String() + `"}`: 0.02,

		`awl_p2p_bandwidth_bytes_total{direction="in",protocol="/awl/kad/1.0.0"}`:  100,
		`awl_p2p_bandwidth_bytes_total{direction="out",protocol="/awl/kad/1.0.0"}`: 200,
		`awl_p2p_bandwidth_bytes_total{direction="in",protocol="unknown"}`:         5,
		`awl_p2p_bandwidth_bytes_total{direction="out",protocol="unknown"}`:        6,
	}, withoutTime(values))

	require.InDelta(t, float64(time.Now().Unix()), values["awl_node_start_timestamp"], 2)
	require.GreaterOrEqual(t, values["awl_node_uptime_seconds"], 0.0)
}

// collect gathers the node gauges as a registry would. The keys are the series
// with the label pairs sorted by name, as in the exposition format.
func collect(t *testing.T) map[string]float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.CollectorFunc(collectNode))
	families, err := registry.Gather()
	require.NoError(t, err)

	values := make(map[string]float64)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			pairs := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				pairs = append(pairs, fmt.Sprintf("%s=%q", label.GetName(), label.GetValue()))
			}
			slices.Sort(pairs)
			key := family.GetName()
			if len(pairs) > 0 {
				key += "{" + strings.Join(pairs, ",") + "}"
			}

			value := metric.GetGauge().GetValue()
			if counter := metric.GetCounter(); counter != nil {
				value = counter.GetValue()
			}
			values[key] = value
		}
	}
	return values
}

// withoutTime drops the series that depend on the clock.
func withoutTime(values map[string]float64) map[string]float64 {
	values = maps.Clone(values)
	delete(values, "awl_node_start_timestamp")
	delete(values, "awl_node_uptime_seconds")
	return values
}

type fakeConfig struct {
	total, confirmed int
	peerIDs          []peer.ID
}

func (f fakeConfig) GetKnownPeersSnapshot() (total, confirmed int, peerIDs []peer.ID) {
	return f.total, f.confirmed, f.peerIDs
}

func (f fakeConfig) GetBlockedPeersCount() int { return 1 }

func (f fakeConfig) GetAuthRequestCounts() (ingoing, outgoing int) { return 4, 5 }

type fakeP2p struct {
	connected map[peer.ID]bool
	latency   map[peer.ID]time.Duration
	bandwidth map[protocol.ID]libp2pmetrics.Stats
}

func (f fakeP2p) ConnectedPeersCount() int                { return 7 }
func (f fakeP2p) OpenConnectionsCount() int               { return 8 }
func (f fakeP2p) OpenStreamsCount() int64                 { return 9 }
func (f fakeP2p) RoutingTableSize() int                   { return 10 }
func (f fakeP2p) BootstrapPeersStats() (int, int)         { return 4, 3 }
func (f fakeP2p) IsConnected(id peer.ID) bool             { _, ok := f.connected[id]; return ok }
func (f fakeP2p) HasDirectConnection(id peer.ID) bool     { return f.connected[id] }
func (f fakeP2p) GetPeerLatency(id peer.ID) time.Duration { return f.latency[id] }
func (f fakeP2p) NetworkStatsByProtocol() map[protocol.ID]libp2pmetrics.Stats {
	return f.bandwidth
}
