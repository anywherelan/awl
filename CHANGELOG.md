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

[0.20.0]: https://github.com/anywherelan/awl/compare/v0.19.0...v0.20.0
