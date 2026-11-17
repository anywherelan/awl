// Package netstate owns the platform-specific OS network state behind AWL's
// VPN gateway feature:
//
//   - Socket marking: keeps libp2p and SOCKS5 exit-node traffic out of the
//     VPN tunnel so gateway mode cannot loop it back into itself. Linux:
//     SO_MARK + ip-rule policy routing. Android: VpnService.protect()
//     callback supplied by the host app. Windows: IP_UNICAST_IF binding to
//     the physical uplink NIC. Other platforms: no-op.
//   - Client-side gateway routes: the TUN default route plus the IPv6
//     fail-closed fence.
//   - Server-side exit-node NAT: forwarding, address translation and the
//     LAN/CGNAT isolation filter.
//   - Uplink detection (Windows): picking the physical interface carrying
//     the best default route, shared by socket marking and server NAT.
//
// All of it is owned by a single Manager instance. Manager is a per-platform
// type: each GOOS slice (manager_linux.go, manager_windows.go,
// manager_android.go, manager_other.go) declares its own struct with the same
// method set, so the shared state behind marking, routes and NAT lives under
// one lock per platform. The cross-platform contract is the method set
// itself, enforced by the compiler through the consumer-declared interfaces
// (awl.NetManager is the full union; service.NetManager and
// service.SocketMarker are subsets).
//
// The Application creates the Manager (NewManager, or NewAndroidManager on
// Android), starts it once per process and feeds its ControlFunc to the
// libp2p host config; the gateway service applies and reverts the client
// routes / server NAT through it at runtime.
package netstate
