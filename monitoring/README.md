# Anywherelan Monitoring

Prometheus + Grafana monitoring stack for Anywherelan (awl). Includes the awl dashboards and the built-in [libp2p dashboards](https://github.com/libp2p/go-libp2p/tree/master/dashboards), see [Dashboards](#dashboards).

## Quick Start

awl exposes all metrics at `http://localhost:8639/metrics` by default.

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

awl exposes pprof endpoints at `http://localhost:8639/api/v0/debug/pprof/`. **Grafana Alloy** takes the CPU, heap and goroutine profiles once a minute and pushes them to **Pyroscope** for storage and querying.

In Grafana, the profiles are on the **awl Profiles** dashboard. In **Explore**, select the Pyroscope datasource, a profile type such as `process_cpu`, `memory` or `goroutine`, and `service_name="anywherelan"`.

You can also browse profiles directly in the Pyroscope UI at http://localhost:4040.

## Metrics Reference

### Node Metrics (`awl_node_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_node_info` | GaugeVec | Static info (=1), labels: `version, peer_id, go_version, os, arch, app`. `app` is `awl`, `awl-tray` or `android` |
| `awl_node_uptime_seconds` | Gauge | Time since the node started. Stopping and starting the server in awl-tray or on Android starts it over |
| `awl_node_start_timestamp` | Gauge | Unix timestamp of node start |

### Peer Metrics (`awl_peers_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_peers_known_total` | Gauge | Total known peers |
| `awl_peers_connected` | GaugeVec | Connected known peers, labels: `path={direct,relay}`: with a direct connection, or only through a relay |
| `awl_peers_confirmed_total` | Gauge | Confirmed (mutual) peers |
| `awl_peers_blocked_total` | Gauge | Blocked peers |
| `awl_peers_auth_requests_ingoing` | Gauge | Pending ingoing auth requests |
| `awl_peers_auth_requests_outgoing` | Gauge | Pending outgoing auth requests |
| `awl_peers_auth_requests_sent_total` | Counter | Auth requests sent |
| `awl_peers_auth_requests_received_total` | Counter | Auth requests received |
| `awl_peers_connection_events_total` | CounterVec | Events, labels: `event={connected,disconnected}` |
| `awl_peers_status_requests_sent_total` | Counter | Status requests sent |
| `awl_peers_status_requests_received_total` | Counter | Status requests received |
| `awl_peers_invites_redeemed_total` | Counter | Invite links redeemed by peers |
| `awl_peers_invite_tokens_rejected_total` | CounterVec | Invites refused, labels: `reason={expired,revoked,used_up,unknown}` |
| `awl_peers_received_requests_dropped_total` | CounterVec | Auth and status requests from other peers that were not handled, labels: `request={auth,status}`, `reason={queue_full,message_too_large,read_timeout,read_error}`. `queue_full`: too many friend requests already wait for an accept |

### VPN Metrics (`awl_vpn_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_vpn_packets_sent_total` | Counter | Packets written to tunnel streams |
| `awl_vpn_packets_received_total` | Counter | Packets received from tunnel streams |
| `awl_vpn_bytes_sent_total` | Counter | Bytes sent via tunnel |
| `awl_vpn_bytes_received_total` | Counter | Bytes received from tunnel |
| `awl_vpn_packets_dropped_total` | CounterVec | Dropped packets, labels: `reason`. On the way to peers: `peer_offline`, `stream_error`, `unknown_address` (no peer has the address), `gateway_client_disabled` (a packet to the internet with the gateway client off, for example from an app bound to every interface), `outbound_channel_full`, `gateway_channel_full`. From peers: `inbound_channel_full`, `parse_error`, `ipv6_disabled`, `gateway_server_disabled`, `gateway_not_allowed`, `gateway_return_from_non_gateway` |
| `awl_vpn_tun_read_errors_total` | Counter | TUN read errors |
| `awl_vpn_tun_write_errors_total` | Counter | TUN write errors |
| `awl_vpn_stream_open_errors_total` | Counter | Tunnel stream open errors |

### SOCKS5 Metrics (`awl_socks5_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_socks5_connections_total` | CounterVec | Connections, labels: `direction={client,server}` |
| `awl_socks5_active_connections` | GaugeVec | Active connections, labels: `direction` |
| `awl_socks5_errors_total` | CounterVec | Errors, labels: `direction, reason`. On the exit node (`server`), the refusals by its rules are here too: `loopback_address`, `private_address`, `link_local_address`, `multicast_address`, `awl_address`, `invalid_address`, `awl_name` |
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
| `awl_p2p_peer_latency_seconds` | GaugeVec | Latency of connected known peers, labels: `peer_id`. A peer has it after the first ping |
| `awl_p2p_bandwidth_bytes_total` | CounterVec | libp2p traffic, labels: `protocol, direction={in,out}`. `protocol` is `unknown` before a stream agrees on one |
| `awl_p2p_redials_total` | CounterVec | Redials of a known peer right after it disconnected, labels: `result` (`success`, `failure`, `peer_connected`: the peer came back on its own first) |
| `awl_p2p_redial_duration_seconds` | HistogramVec | How long the dial of a redial took, labels: `result` |
| `awl_p2p_reconnect_resets_total` | CounterVec | Times the reconnection schedule was reset because the network came back, labels: `reason` (`bootstrap_returned`, `notified`) |

### DNS Metrics (`awl_dns_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_dns_queries_total` | CounterVec | Queries, labels: `handler={awl,awl_ptr,proxy}`: `.awl` names, reverse lookups answered from the names of peers, queries forwarded upstream |
| `awl_dns_query_errors_total` | Counter | Upstream DNS failures |
| `awl_dns_query_duration_seconds` | HistogramVec | Query duration, labels: `handler` |
| `awl_dns_intercept_packets_total` | Counter | DNS packets taken from the tunnel into the netstack bridge (Android) |
| `awl_dns_intercept_dropped_total` | CounterVec | Intercepted DNS packets dropped, labels: `reason={netstack_write,tun_write}` |

### API Metrics (`awl_api_*`)

| Metric | Type | Description |
|---|---|---|
| `awl_api_requests_total` | CounterVec | Requests, labels: `code, method, host, url` |
| `awl_api_request_duration_seconds` | HistogramVec | Duration, labels: `code, method, host, url` |
| `awl_api_request_size_bytes` | HistogramVec | Request size, labels: `code, method, host, url` |
| `awl_api_response_size_bytes` | HistogramVec | Response size, labels: `code, method, host, url` |

Requests to `/metrics` and to pprof by Prometheus and Alloy are counted here too.

### libp2p Built-in Metrics

Enabled via `libp2p.PrometheusRegisterer`. Prefixed with `libp2p_`. Includes swarm, identify, autonat, autorelay, holepunch, relay service, eventbus, and resource manager metrics.

### DHT Metrics (`libp2p_kad_dht_*`)

RPC metrics of `go-libp2p-kad-dht`. The library reports them through OpenTelemetry; `metrics.ExportKadDHTMetrics` attaches a Prometheus exporter that writes them next to the libp2p metrics. All `rpc_*` metrics carry the label `message_type` (`FIND_NODE`, `PING`, ...). A metric shows up once it has data.

| Metric | Type | Description |
|---|---|---|
| `libp2p_kad_dht_rpc_outbound_requests_total` | Counter | Requests sent, failed ones included |
| `libp2p_kad_dht_rpc_outbound_request_errors_total` | Counter | Requests that failed |
| `libp2p_kad_dht_rpc_outbound_request_latency_milliseconds` | Histogram | Time until the response |
| `libp2p_kad_dht_rpc_outbound_messages_total` | Counter | One-way messages sent, no response expected |
| `libp2p_kad_dht_rpc_outbound_message_errors_total` | Counter | One-way messages that failed |
| `libp2p_kad_dht_rpc_outbound_bytes` | Histogram | Size of sent messages |
| `libp2p_kad_dht_rpc_inbound_messages_total` | Counter | Messages received, i.e. requests served for other peers |
| `libp2p_kad_dht_rpc_inbound_message_errors_total` | Counter | Received messages that could not be read or handled |
| `libp2p_kad_dht_rpc_inbound_bytes` | Histogram | Size of received messages |
| `libp2p_kad_dht_rpc_inbound_request_latency_milliseconds` | Histogram | Time to handle a received request |
| `libp2p_kad_dht_network_size` | Gauge | Estimated number of peers in the DHT |

## Dashboards

Dashboards in `dashboards/` are provisioned automatically with the Docker Compose setup. Each subdirectory becomes a Grafana folder, and Grafana picks up changed files within 10 seconds, no restart needed.

### `awl/`

They show every node by default. The `instance` variable picks some of them. awl Profiles shows one node at a time. Node restarts are marked on the graphs, and the dashboards link to each other.

- **awl Overview**: node info and uptime; process CPU, memory, goroutines, file descriptors, GC pauses; known, confirmed and connected peers, direct or through a relay, connection events, latency of the slowest peers, auth requests, status exchange, invites, dropped auth and status requests; libp2p connections and streams, bootstrap peers, DHT routing table, redials after a disconnect, resets of the reconnection schedule.
- **awl Traffic**: libp2p traffic by protocol; VPN tunnel throughput, packets, drops by reason and their share, TUN and stream errors; SOCKS5 connections, errors and refusals of the exit node, durations; DNS queries, latency by handler and errors; local API requests, latency and response sizes.
- **awl DHT**: Kademlia RPCs. Lookups we make: rate, error share, latency, per-minute bursts. Requests we serve: rate, latency, errors. Routing table and network size, DHT traffic.
- **awl Connectivity**: how the node reaches peers. Reachability by AutoNAT and of each own address, the number of reachable addresses, AutoRelay state; open connections by transport and the relayed share, new connections, handshake latency, redials; hole punching outcomes and success share; dial errors, dials per peer, black hole filters, resource manager blocks; relay reservations the node holds as a client.
- **awl Profiles**: CPU, heap, allocation and goroutine flame graphs from Pyroscope for one node, with the matching Prometheus metrics next to them. Flame graphs merge the whole selected time range: narrow it to a spike to see what caused it.

Series of one node are merged by `instance`, so renaming a scrape job does not show the node twice.

### `bootstrap/`

- **awl Bootstrap Node**, for [awl-bootstrap-node](https://github.com/anywherelan/awl-bootstrap-node): the node and its links to the other bootstrap nodes; clients by version and OS; the relay service: reservations, circuits, relayed data and how often the relay limit closes a circuit; traffic by protocol and connections; DHT and AutoNAT requests the node serves; process resources. The node serves its metrics on its HTTP address, port `9010` by default.

### `libp2p/`

Copied from [libp2p/go-libp2p](https://github.com/libp2p/go-libp2p/tree/master/dashboards): AutoNAT, AutoNATv2, AutoRelay, Eventbus, Holepunch, Host Addrs, Identify, Relay Service, Resource Manager, Swarm.

## Configuration

The Prometheus config (`prometheus.yml`) scrapes awl at `host.docker.internal:8639/metrics`, and Alloy (`alloy/config.alloy`) takes the profiles from the same address. If awl runs on a different port, edit the target in both files.

The dashboards name the node by its `instance` label, `local` in both files. If you rename it, change it in both files, or awl Profiles will not find the profiles.
