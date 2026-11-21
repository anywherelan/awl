# Changelog

All notable user-facing changes to Anywherelan are documented in this file. It covers the
whole product: the `awl` and `awl-tray` binaries from this repository, and the Android app
and web UI from [awl-flutter](https://github.com/anywherelan/awl-flutter), which are
released together with them.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0,
a minor version may contain breaking changes. They are listed under **Changed** or
**Removed**.

Releases before v0.20.0 are described on the
[GitHub releases page](https://github.com/anywherelan/awl/releases).

## [0.21.0] - 2026-11-21

### Added

- **VPN addresses based on device IDs.** A new device derives its VPN address from its ID
  and shares it with its peers. Other devices running awl v0.21.0 or newer can usually
  reach it at that same address. A new peer's IP appears after the first status exchange.
  ([#240](https://github.com/anywherelan/awl/issues/240),
  [#317](https://github.com/anywherelan/awl/pull/317))
- **Direct connections in a local network.** Devices in the same local network
  can connect directly. Update Android devices to v0.21.0 so they can share their
  local network addresses. If a VPN tunnel is already using a relay, it switches to
  the direct connection when one becomes available.
  ([#313](https://github.com/anywherelan/awl/pull/313),
  [#326](https://github.com/anywherelan/awl/pull/326),
  [#27](https://github.com/anywherelan/awl/issues/27),
  [#270](https://github.com/anywherelan/awl/issues/270),
  [#301](https://github.com/anywherelan/awl/issues/301),
  [#303](https://github.com/anywherelan/awl/issues/303))
- **Grafana dashboards for awl.** The monitoring stack in `monitoring/` has the
  *awl Overview*, *awl Traffic*, *awl DHT*, *awl Connectivity* and *awl Profiles*
  dashboards, and *awl Bootstrap Node* for the bootstrap nodes. The libp2p dashboards
  moved to the `libp2p` folder. ([#308](https://github.com/anywherelan/awl/pull/308))
- `vpn.mtu` in the config sets the MTU of the VPN interface, from 1280 to 3500.
  It also applies on Android. Thanks to @officialasishkumar.
  ([#274](https://github.com/anywherelan/awl/issues/274),
  [#315](https://github.com/anywherelan/awl/pull/315),
  [awl-flutter#45](https://github.com/anywherelan/awl-flutter/pull/45))
- Monitoring: new metrics and labels. `awl_peers_connected` tells peers connected
  directly from peers connected only through a relay. `awl_p2p_bandwidth_bytes_total`
  counts libp2p traffic by protocol. `awl_peers_received_requests_dropped_total` counts
  friend requests and status exchanges from other peers that awl dropped, for example
  when too many friend requests already wait. `awl_vpn_packets_dropped_total` has new
  reasons, such as `peer_offline`. `awl_socks5_errors_total` counts the requests an exit
  node refused by its rules. `awl_node_info` shows the app: `awl`, `awl-tray` or
  `android`. `awl_dns_query_duration_seconds` is split by handler.
  ([#308](https://github.com/anywherelan/awl/pull/308))
- `awl_p2p_lan_dials_total` counts attempts to connect to known peers at their
  local network addresses. ([#313](https://github.com/anywherelan/awl/pull/313))

### Changed

- The default VPN subnet is now `10.64.0.0/11`, from `10.64.0.0` to `10.95.255.255`.
  On upgrade, awl widens the old defaults `10.66.0.1/16` and `10.66.0.1/24` to
  `10.66.0.1/11`. Existing device and peer addresses are kept.

  If the new subnet overlaps another network you use, stop awl and set `vpn.ipNet` config to
  a subnet that avoids the overlap. If your device uses `10.66.0.1`, for example,
  `10.66.0.1/20` keeps its IP and uses a smaller subnet. Remove `ipAddr` from any entries
  under `knownPeers` whose address is outside the new subnet. They get a new address at
  the next status exchange.

  To switch this device to an address based on its ID, stop awl, set `vpn.ipNet` to
  the subnet's network address, such as `10.64.0.0/11`, and start awl. The startup log
  shows the new VPN address. To use it on your other devices too, update
  *Local IP address* in this device's *Peer settings* on each of them.
  ([#317](https://github.com/anywherelan/awl/pull/317))
- awl doesn't start if `vpn.ipNet` in the config is not an IPv4 network or its prefix is
  longer than `/30`. The error says what is wrong. Before, awl quietly used the default
  network instead. ([#317](https://github.com/anywherelan/awl/pull/317))
- A new install has an MTU of 1400 on the VPN interface. The smaller MTU helps prevent
  stalled downloads through a VPN gateway. On upgrade, a config without `vpn.mtu`
  keeps 3500. If your devices use a mix of 1400 and 3500, use 1400 on all of them,
  including exit nodes. To change it, stop awl, set `vpn.mtu` in the config, and
  restart awl. ([#327](https://github.com/anywherelan/awl/pull/327))
- With **Serve as VPN Gateway** on, a Windows gateway with MTU 1400 can still stall
  downloads for clients with MTU 3500. Update those clients and set their `vpn.mtu`
  to `1400`.
  ([#327](https://github.com/anywherelan/awl/pull/327))
- awl doesn't start if `vpn.mtu` in the config is not between 1280 and 3500. The error
  says what is wrong. ([#327](https://github.com/anywherelan/awl/pull/327))
- awl connects to the first known device up to 4 seconds sooner after startup.
  ([#325](https://github.com/anywherelan/awl/pull/325))
- Android uses DHT client mode by default, including after an update. Most phones
  were already DHT clients. Some phones with a public address could act as DHT servers
  and spend mobile data answering other devices' discovery requests. The new default
  skips those replies and still lets you find your devices. Set `p2pNode.dhtMode` to
  `auto` in the config to restore automatic mode selection.
  ([#332](https://github.com/anywherelan/awl/pull/332))
- When a relay cuts the connection to a device, awl reconnects about a second sooner.
  Before, the two devices often dialed each other at the same moment and ended up with
  two or more connections through relays. ([#326](https://github.com/anywherelan/awl/pull/326))
- VPN traffic takes up to 7% less CPU. Where the CPU limits the speed, for example on a
  slow device, the VPN is a bit faster. ([#311](https://github.com/anywherelan/awl/pull/311))
- SOCKS5 proxy: when the exit node is offline, a connection through the proxy fails right
  away. Before, awl tried to reach the exit node for each such connection. A browser
  opens many of them, and this added up to hundreds of connection attempts a minute.
  awl reconnects to the exit node in the background, the same way as to your other
  devices. ([#325](https://github.com/anywherelan/awl/pull/325))
- When the selected VPN gateway has no awl address or is no longer a known peer,
  awl starts with the VPN gateway off.
  ([#317](https://github.com/anywherelan/awl/pull/317),
  [awl-flutter#45](https://github.com/anywherelan/awl-flutter/pull/45))
- The web UI and Android app show a new peer's `.awl` name while waiting for its first
  address. You can still save changes in *Peer settings* with *Local IP address* empty.
  ([awl-flutter#45](https://github.com/anywherelan/awl-flutter/pull/45))
- awl no longer conflicts over DNS settings with some other VPN apps on the same
  computer. If an older awl was killed or crashed just before the upgrade, its DNS
  settings can stay behind and break DNS. On Linux, stop awl and fix `/etc/resolv.conf`.
  The older awl left a backup of it in `/etc`. If there is no backup, restart the
  computer. On Windows, if DNS doesn't work while awl is stopped, remove the NRPT rule
  for `.`. ([#312](https://github.com/anywherelan/awl/pull/312))
- The API returns an empty `IpAddr` for a peer awaiting its first address.
  `/peers/update_settings` keeps the address when the submitted `ipaddr` is empty.
  A friend request includes `ipv4Addr`, the address the peer announces, and
  `SuggestedIP` shows the address it would get. ([#317](https://github.com/anywherelan/awl/pull/317))
- Monitoring: `awl_p2p_peer_latency_seconds` is reported only for connected known peers,
  after the first ping. Before, offline peers had a zero. awl no longer goes over all
  known peers every 2 seconds to update the metrics: they are read when Prometheus
  scrapes them. The new `path`, `app` and `handler` labels start new series.
  ([#308](https://github.com/anywherelan/awl/pull/308))
- Monitoring: the stack scrapes awl every 10 seconds instead of 3 and takes profiles
  once a minute. Mutex and block profiles are no longer taken, awl doesn't record them.
  The node is shown as `local`. ([#308](https://github.com/anywherelan/awl/pull/308))
- Updated Go to 1.27.4 and dependencies.
  ([#312](https://github.com/anywherelan/awl/pull/312),
  [#332](https://github.com/anywherelan/awl/pull/332))

### Removed

- Monitoring: `awl_socks5_bytes_transferred_total`. It was never filled.
  ([#308](https://github.com/anywherelan/awl/pull/308))

### Fixed

- Large UDP datagrams now get through the VPN when they are split into multiple IP
  packets.
  ([#311](https://github.com/anywherelan/awl/pull/311))
- VPN: after awl reconnected to a device, the first packets to it were lost. With a
  device connected through a relay this could happen each time the relay cut the
  connection. ([#326](https://github.com/anywherelan/awl/pull/326))
- Linux: awl could fail to start with `unable to set IP ... file exists` when
  NetworkManager had saved settings for `awl0`. This happened, for example, after you put
  `awl0` in a firewalld zone. Thanks to @VoNguyenMinhTriet.
  ([#307](https://github.com/anywherelan/awl/issues/307),
  [#309](https://github.com/anywherelan/awl/pull/309))
- NixOS and Guix: awl couldn't serve as an exit node and failed with `iptables ...
  not found in $PATH` when started from awl-tray or as a systemd service.
  ([#263](https://github.com/anywherelan/awl/issues/263),
  [#321](https://github.com/anywherelan/awl/pull/321))
- Linux: if Docker started after awl, turning off **Serve as VPN Gateway** or stopping awl
  could leave Docker containers without network access. awl now leaves IPv4 forwarding
  enabled when the VPN gateway stops. ([#324](https://github.com/anywherelan/awl/pull/324))
- Linux: with **Serve as VPN Gateway** on, other awl devices sometimes couldn't reach the
  ports that Docker publishes on the device, for example after awl restarted. Now these
  ports work the same as with the switch off, and apps in containers see the awl address
  of the device that connects. ([#324](https://github.com/anywherelan/awl/pull/324))
- Linux: on some systems, awl could hang when you turned **Serve as VPN Gateway** on or
  off, or when awl stopped, while another program such as Docker was changing the
  firewall. Now awl waits for it at most 10 seconds. If the firewall is still busy,
  turning the switch on fails with an error.
  ([#324](https://github.com/anywherelan/awl/pull/324))
- Linux: downloads through a device with **Serve as VPN Gateway** on could stall when its
  internet connection had a smaller MTU, for example PPPoE.
  ([#327](https://github.com/anywherelan/awl/pull/327))
- awl crashed when the port of the web UI was already taken. Now it stops with the error
  `address already in use`. ([#322](https://github.com/anywherelan/awl/pull/322))
- SOCKS5 proxy: a removed device stayed selected as the exit node. Now the exit node is
  cleared when you remove the device.
  ([#325](https://github.com/anywherelan/awl/pull/325))
- A failed attempt to add a blocked device leaves it blocked.
  ([#332](https://github.com/anywherelan/awl/pull/332))
- Linux: awl runs without root again when the VPN interface is off
  (`vpn.disableVPNInterface`). Since v0.18.0 it couldn't connect to other devices this way.
  ([#322](https://github.com/anywherelan/awl/pull/322))
- Linux without systemd-resolved: while awl ran, short names of devices in your local
  network, like `nas` for `nas.lan`, didn't resolve. Now they do.
  ([#323](https://github.com/anywherelan/awl/pull/323))
- On Linux, when awl manages `/etc/resolv.conf` directly, it restores its DNS settings
  if another program overwrites that file. On Windows, DNS setup no longer waits for
  missing interface settings to appear.
  ([#312](https://github.com/anywherelan/awl/pull/312))
- CLI: `awl cli gateway status` showed `Connection: direct` for a gateway that was not
  connected. ([#325](https://github.com/anywherelan/awl/pull/325))
- Monitoring: a reverse DNS lookup that awl forwarded upstream was counted twice, as
  `awl_ptr` and as `proxy`. ([#308](https://github.com/anywherelan/awl/pull/308))
- Monitoring on Linux: Pyroscope stopped answering after the LAN address of the computer
  changed. ([#308](https://github.com/anywherelan/awl/pull/308))

## [0.20.0] - 2026-10-24

### Added

- **Invite links.** The device that opens an invite link is added without a manual
  accept, with the name and exit node permission you chose. By default a link works once
  and expires in 24 hours. The *One-time link* in *My ID* expires in 12 hours. You can
  change both and revoke a link. If a link no longer works, opening it sends an ordinary
  friend request. In the web UI and the Android app: the new *Invite links* screen and
  Android deep links. In the CLI: `peers invite create`, `peers invite list`,
  `peers invite revoke` and `peers add --link`. In the API: `peers/invites/create`,
  `peers/invites/list` and `peers/invites/revoke`. Every device also has its own link,
  shown in *My ID* and by `awl cli me id`. Opening it sends a friend request with the
  name already filled in.
  ([#269](https://github.com/anywherelan/awl/pull/269),
  [awl-flutter#39](https://github.com/anywherelan/awl-flutter/pull/39))
- **`.awl` names work on Android.** `ping laptop.awl` and `http://nas.awl` work from the
  phone. Other DNS queries go to `1.1.1.1` by default, and through the tunnel when the VPN
  gateway is on. `dns.disableDNS` in the config turns this off. With Android's Private DNS
  in strict mode, `.awl` names don't resolve. LAN names from your router don't resolve
  while awl's DNS is on. See *Platform notes & known limitations* in the README.
  ([#17](https://github.com/anywherelan/awl/issues/17),
  [#264](https://github.com/anywherelan/awl/pull/264),
  [awl-flutter#38](https://github.com/anywherelan/awl-flutter/pull/38))
- **macOS app.** `awl-tray` for macOS is now an `Anywherelan.app` bundle in a `.dmg`,
  for Apple Silicon (`arm64`) and Intel (`amd64`). If you start the app right from the
  disk image window, it asks to move it to Applications first.
  ([#279](https://github.com/anywherelan/awl/pull/279))
- App: incoming friend requests are listed at the top of the peer list, with a
  *Respond* button. ([awl-flutter#43](https://github.com/anywherelan/awl-flutter/pull/43))
- Blocked peers can be unblocked: API `peers/unblock`, CLI `peers blocked` and
  `peers unblock`, and a redesigned *Blocked peers* screen in the app. An unblocked peer
  is not added back. Its next request shows up as an ordinary incoming one.
  ([#286](https://github.com/anywherelan/awl/pull/286),
  [#288](https://github.com/anywherelan/awl/pull/288),
  [awl-flutter#41](https://github.com/anywherelan/awl-flutter/pull/41))
- You can allow a peer to use this device as an exit node right when you add it, accept
  its request or create an invite link. This works in the app, in the CLI with
  `peers add --allow-exit-node` and in the API with `AllowUsingAsExitNode` in
  `peers/invite_peer` and `peers/accept_peer`.
  ([#269](https://github.com/anywherelan/awl/pull/269))
- Peer domain names can use letters of any script. `ноутбук.awl` works in browsers;
  command-line tools may need its ASCII form `xn--90asheufc.awl`.
  ([#290](https://github.com/anywherelan/awl/pull/290))
- App: the *Diagnostics* screen can share logs and debug info on Android and download
  them in the web UI. ([awl-flutter#42](https://github.com/anywherelan/awl-flutter/pull/42))
- Experimental IPv6 between peers, off by default. To try it, set `vpn.ipNetV6` in the
  config by hand. It works only on Linux and macOS, and the VPN gateway still carries
  only IPv4. Thanks to @NNdroid.
  ([#262](https://github.com/anywherelan/awl/pull/262),
  [#282](https://github.com/anywherelan/awl/pull/282))
- CLI: `peers decline` declines a friend request. `peers add` accepts a request by the
  name shown in `peers requests`, and uses that name by default.
  ([#288](https://github.com/anywherelan/awl/pull/288))
- Monitoring: metrics for kad-dht (`libp2p_kad_dht_*`), for redials and reconnects
  (`awl_p2p_redials_total`, `awl_p2p_redial_duration_seconds`,
  `awl_p2p_reconnect_resets_total`), for invite links and for DNS on Android.
  `peers/get_known` and `debug/p2p_info` show the reconnection state and known addresses
  of each peer.
  ([#264](https://github.com/anywherelan/awl/pull/264),
  [#269](https://github.com/anywherelan/awl/pull/269),
  [#285](https://github.com/anywherelan/awl/pull/285))

### Changed

- **Much less background traffic to offline peers.** awl used to look up each offline
  peer in the DHT every 25 seconds. Now it retries peers and bootstrap nodes it can't
  reach less and less often, from every 20 seconds up to every 10 minutes. A peer that
  disconnects is redialed within seconds. On a laptop with 10 offline peers, inbound DHT
  traffic dropped from 1.8 GiB to about 140 MB a day. A peer that comes back but can't
  connect to you itself may take up to 10 minutes to show up.
  ([#285](https://github.com/anywherelan/awl/pull/285))
- Peer domain names are normalized and unique. They are lowercase, with `_` in place of
  spaces and only characters a hostname can have. A name that another peer, a peer ID or
  `admin` already has gets a suffix (`nas_0`). Existing configs are fixed on load: of two
  peers with the same name, the one added first keeps it. The API rejects a domain name
  that is already taken. ([#290](https://github.com/anywherelan/awl/pull/290))
- The SOCKS5 exit node no longer gives access to its own LAN and its awl peers, the same
  as the VPN gateway. Access to the LAN may come back as an option, see
  [#268](https://github.com/anywherelan/awl/issues/268).
  ([#291](https://github.com/anywherelan/awl/pull/291))
- The SOCKS5 exit node is no longer chosen automatically. Before, if you hadn't chosen
  one, the first peer that allowed you became your exit node. Now you always choose it
  yourself, as with the VPN gateway.
  ([#269](https://github.com/anywherelan/awl/pull/269))
- Some traffic of the local SOCKS5 proxy bypasses the exit node: `.awl` names, addresses
  in your awl network and your own `localhost`. They work through the proxy even when no
  exit node is selected. This applies only to apps on the same device. Connecting to an
  address that doesn't answer now times out after 30 seconds.
  ([#291](https://github.com/anywherelan/awl/pull/291))
- The local SOCKS5 proxy of a new install listens on `127.0.0.1:1066` instead of
  `127.0.0.66:8080`, a port many other apps use. Existing configs keep their address.
  ([#271](https://github.com/anywherelan/awl/issues/271),
  [#292](https://github.com/anywherelan/awl/pull/292))
- Updates are checked every 48 hours instead of every 8, and at every start as before.
  Configs with the old defaults `8h` or `24h` switch to the new one.
  ([#292](https://github.com/anywherelan/awl/pull/292))
- App: friend request notifications start with the sender's name. They are withdrawn
  when the request is answered elsewhere, for example from the CLI, or cancelled.
  ([awl-flutter#43](https://github.com/anywherelan/awl-flutter/pull/43))
- Peer names received from other peers are cleaned up: control characters are dropped,
  spaces trimmed, and the name is cut to 100 characters.
  ([#290](https://github.com/anywherelan/awl/pull/290))
- macOS: *Check for updates* opens the release page instead of updating in place, and
  `awl cli update` prints the link. The tray icon now shows up on macOS 26, and the
  password prompt says why administrator rights are needed.
  ([#279](https://github.com/anywherelan/awl/pull/279))
- CLI: `awl cli --help` and `awl -h` describe what awl does and show examples. The help
  texts of all commands are reworked. `peers remove` now says it also blocks the peer.
  ([#288](https://github.com/anywherelan/awl/pull/288))
- API: `FriendRequestReply.Alias` is required only to accept a request, not to decline
  it. ([#288](https://github.com/anywherelan/awl/pull/288))
- Updated Go to 1.26.8 and dependencies.
  ([#283](https://github.com/anywherelan/awl/pull/283))

### Removed

- macOS 12 Monterey and older are no longer supported.
- The macOS `.zip` downloads are replaced by `.dmg`.
  ([#279](https://github.com/anywherelan/awl/pull/279))

### Fixed

- TCP hole punching didn't work on Linux, Windows and Android since v0.18.0, so more
  connections than needed went through relays.
  ([#284](https://github.com/anywherelan/awl/pull/284))
- Two peers that added each other by hand could both stay "not confirmed" for minutes.
  ([#287](https://github.com/anywherelan/awl/pull/287))
- Killing awl while it was saving the config could destroy the file, and with it the
  private key and the list of peers. This can't happen any more. A config that can't be
  parsed is kept as `config_awl.json.corrupt-<time>` instead of being silently replaced
  with a new identity. ([#266](https://github.com/anywherelan/awl/pull/266))
- awl didn't start at all when another app held the port of the local SOCKS5 proxy. Now
  only the proxy stays off: the app shows it as stopped, and the log says which address
  is taken. ([#271](https://github.com/anywherelan/awl/issues/271),
  [#292](https://github.com/anywherelan/awl/pull/292))
- awl didn't start when the peer chosen as the VPN gateway had stopped allowing you, and
  awl-tray couldn't start at all until the setting was removed from the config. Now awl
  starts with the gateway on and writes a warning to the log. Traffic flows again once
  the peer allows you back. ([#290](https://github.com/anywherelan/awl/pull/290))
- awl-tray and the Android app crashed instead of reporting the error when awl failed to
  start, for example with a broken key in the config or a taken listen port.
  ([#290](https://github.com/anywherelan/awl/pull/290))
- App: forms silently did nothing while the server was stopped or unreachable. Now they
  show an error. Two misleading labels in peer settings are fixed.
  ([#272](https://github.com/anywherelan/awl/issues/272),
  [awl-flutter#42](https://github.com/anywherelan/awl-flutter/pull/42))
- Web UI: copy buttons didn't work on `http://admin.awl`.
  ([#200](https://github.com/anywherelan/awl/issues/200),
  [awl-flutter#40](https://github.com/anywherelan/awl-flutter/pull/40))
- Android: notifications never worked in release builds, so incoming friend requests
  were easy to miss. ([awl-flutter#43](https://github.com/anywherelan/awl-flutter/pull/43))
- macOS: *Check for updates* never found a new version. Paths with spaces broke the
  administrator prompt ([#182](https://github.com/anywherelan/awl/issues/182)). The app
  could refuse to start because the binary required the newest macOS.
  ([#279](https://github.com/anywherelan/awl/pull/279))
- If the headless `awl` failed to start, it left behind the system DNS settings and
  gateway routes it had already changed.
  ([#290](https://github.com/anywherelan/awl/pull/290))
- Linux: the desktop entry created by `awl-tray` had a blank icon. Thanks to @kg6zvp.
  ([#273](https://github.com/anywherelan/awl/pull/273))
- Linux: `awl` run as root printed an `ifconfig lo0 alias` error on every start. The
  command is for macOS only. ([#292](https://github.com/anywherelan/awl/pull/292))

### Security

- A peer allowed to use this device as a SOCKS5 exit node could reach the device's
  `localhost` through `0.0.0.0` or `[::1]`, and with it the awl API. It could also reach
  the device's LAN, the cloud metadata address of a VPS and the device's other awl
  peers. The exit node now refuses all of these, also when a hostname resolves to such
  an address. ([#291](https://github.com/anywherelan/awl/pull/291))
- Any node could make awl run out of memory with an endless friend request, or keep
  connections open forever. Friend request and status messages now have size limits
  (64 KiB and 512 KiB) and must finish within 10 seconds. The number of such connections
  at a time is limited, overall and per peer. Thanks to EQSTLab for the report. ([GHSA-fg78-6vg7-9m4c](https://github.com/anywherelan/awl/security/advisories/GHSA-fg78-6vg7-9m4c),
  [#290](https://github.com/anywherelan/awl/pull/290))
- Friend requests waiting for a manual accept are limited to 20, so a flood of requests
  from throwaway peer IDs can't fill memory. Invite links and peers that add each other
  are not affected by the limit. ([#290](https://github.com/anywherelan/awl/pull/290))
- A peer name could contain characters that reverse the text direction, such as U+202E,
  to look like a different name in the UI, logs and notifications. They are dropped now,
  and the API rejects a name or alias that contains them.
  ([#290](https://github.com/anywherelan/awl/pull/290))

[0.21.0]: https://github.com/anywherelan/awl/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/anywherelan/awl/compare/v0.19.0...v0.20.0
