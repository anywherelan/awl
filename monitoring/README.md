# AWL Monitoring

Prometheus + Grafana monitoring stack for AWL. Includes built-in [libp2p dashboards](https://github.com/libp2p/go-libp2p/tree/master/dashboards) and AWL-specific metrics.

## Quick Start

AWL exposes all metrics at `http://localhost:8639/metrics` by default.

### macOS

```bash
docker compose -f docker-compose.yml up
```

### Linux

```bash
docker compose -f docker-compose.yml -f docker-compose-linux.yml up
```

Then open:
- **Grafana**: http://localhost:3000 (no login required)
- **Prometheus**: http://localhost:9090
- **Pyroscope**: http://localhost:4040

## Continuous Profiling

AWL exposes pprof endpoints at `http://localhost:8639/api/v0/debug/pprof/`. **Grafana Alloy** scrapes these every 15 seconds (CPU, heap, goroutine, mutex, block profiles) and pushes them to **Pyroscope** for storage and querying.

To view profiles in Grafana: **Explore → select Pyroscope datasource** → pick `process_cpu{app="anywherelan"}` (or `memory`, `goroutine`, etc.).

You can also browse profiles directly in the Pyroscope UI at http://localhost:4040.

## Metrics Reference

### AWL Node Metrics (`awl_node_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_node_info` | GaugeVec | Static info (=1), labels: `version, peer_id, go_version, os, arch` |
| `awl_node_uptime_seconds` | Gauge | Node uptime |
| `awl_node_start_timestamp` | Gauge | Unix timestamp of node start |

### Peer Metrics (`awl_peers_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_peers_known_total` | Gauge | Total known peers |
| `awl_peers_connected` | Gauge | Currently connected known peers |
| `awl_peers_confirmed_total` | Gauge | Confirmed (mutual) peers |
| `awl_peers_blocked_total` | Gauge | Blocked peers |
| `awl_peers_auth_requests_ingoing` | Gauge | Pending ingoing auth requests |
| `awl_peers_auth_requests_outgoing` | Gauge | Pending outgoing auth requests |
| `awl_peers_auth_requests_sent_total` | Counter | Auth requests sent |
| `awl_peers_auth_requests_received_total` | Counter | Auth requests received |
| `awl_peers_connection_events_total` | CounterVec | Events, labels: `event={connected,disconnected}` |

### VPN Metrics (`awl_vpn_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_vpn_packets_sent_total` | Counter | Packets written to tunnel streams |
| `awl_vpn_packets_received_total` | Counter | Packets received from tunnel streams |
| `awl_vpn_bytes_sent_total` | Counter | Bytes sent via tunnel |
| `awl_vpn_bytes_received_total` | Counter | Bytes received from tunnel |
| `awl_vpn_packets_dropped_total` | CounterVec | Dropped packets, labels: `reason` |
| `awl_vpn_tun_read_errors_total` | Counter | TUN read errors |
| `awl_vpn_tun_write_errors_total` | Counter | TUN write errors |
| `awl_vpn_stream_open_errors_total` | Counter | Tunnel stream open errors |

### SOCKS5 Metrics (`awl_socks5_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_socks5_connections_total` | CounterVec | Connections, labels: `direction={client,server}` |
| `awl_socks5_active_connections` | GaugeVec | Active connections, labels: `direction` |
| `awl_socks5_bytes_transferred_total` | CounterVec | Bytes transferred, labels: `direction` |
| `awl_socks5_errors_total` | CounterVec | Errors, labels: `direction, reason` |
| `awl_socks5_connection_duration_seconds` | HistogramVec | Duration, labels: `direction` |

### P2P Network Metrics (`awl_p2p_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_p2p_bootstrap_peers_total` | Gauge | Configured bootstrap peers |
| `awl_p2p_bootstrap_peers_connected` | Gauge | Connected bootstrap peers |
| `awl_p2p_dht_routing_table_size` | Gauge | DHT routing table size |
| `awl_p2p_open_connections` | Gauge | Open connections |
| `awl_p2p_open_streams` | Gauge | Open streams |
| `awl_p2p_connected_peers` | Gauge | All connected peers |
| `awl_p2p_peer_latency_seconds` | GaugeVec | Peer latency, labels: `peer_id` |

### DNS Metrics (`awl_dns_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_dns_queries_total` | CounterVec | Queries, labels: `handler={local,ptr,proxy}` |
| `awl_dns_query_errors_total` | Counter | Upstream DNS failures |
| `awl_dns_query_duration_seconds` | Histogram | Query duration |

### API Metrics (`awl_api_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_api_requests_total` | CounterVec | Requests, labels: `code, method` |
| `awl_api_request_duration_seconds` | HistogramVec | Duration, labels: `code, method` |
| `awl_api_requests_in_flight` | Gauge | In-flight requests |

### libp2p Built-in Metrics

Enabled via `libp2p.PrometheusRegisterer`. Prefixed with `libp2p_`. Includes swarm, identify, autonat, autorelay, holepunch, relay service, eventbus, and resource manager metrics.

Pre-built Grafana dashboards for these metrics are included in `dashboards/`.

## Dashboards

The `dashboards/` directory contains Grafana dashboards copied from [libp2p/go-libp2p](https://github.com/libp2p/go-libp2p/tree/master/dashboards):

- AutoNAT, AutoNATv2, AutoRelay, Eventbus, Holepunch, Host Addrs, Identify, Relay Service, Resource Manager, Swarm

These are automatically provisioned when using the Docker Compose setup.

## Configuration

The Prometheus config (`prometheus.yml`) scrapes AWL at `host.docker.internal:8639/metrics`. If your AWL instance runs on a different port, edit the target accordingly.
