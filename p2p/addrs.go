package p2p

import (
	"net"
	"net/netip"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ipfs/go-log/v2"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/wlynxg/anet"
)

// dockerBridgeName matches the bridges docker creates on Linux: the default
// one and those of user-defined networks, named after the network ID.
var dockerBridgeName = regexp.MustCompile(`^(docker0|br-[0-9a-f]{12})$`)

// localInterface is a network interface of this host.
type localInterface struct {
	name  string
	flags net.Flags
	// the addresses of the interface with the prefix length of their subnets
	prefixes []netip.Prefix
}

// localAddrs knows the network interfaces of this host and decides which of
// its addresses to announce to other peers.
type localAddrs struct {
	logger *log.ZapEventLogger
	// our addresses on the awl interface, set before libp2p.New
	vpnPrefixes []netip.Prefix
	// Network().ListenAddresses of the host, set on Android once libp2p.New
	// returns: the address factory runs on libp2p goroutines
	listenAddrs atomic.Pointer[func() []multiaddr.Multiaddr]
	// readInterfaces, or a fixed list in tests
	read func() ([]localInterface, error)

	ifacesMu sync.Mutex
	// the last list read, shared by the callers: do not change it
	ifaces     []localInterface
	ifacesRead time.Time
}

// interfacesTTL is how long the list of network interfaces is reused. libp2p
// asks for the announced addresses once in 5 seconds and on every inbound
// connection: on a public node about twice a second.
const interfacesTTL = 5 * time.Second

// interfaces lists the network interfaces of the host. It reads them again
// once the last list is older than interfacesTTL.
func (l *localAddrs) interfaces() []localInterface {
	l.ifacesMu.Lock()
	defer l.ifacesMu.Unlock()
	if time.Since(l.ifacesRead) < interfacesTTL {
		return l.ifaces
	}

	ifaces, err := l.read()
	if err != nil {
		l.logger.Warnf("failed to list network interfaces: %v", err)
	}
	l.ifaces = ifaces
	l.ifacesRead = time.Now()

	return ifaces
}

// readInterfaces lists the network interfaces with anet: on Android 11+ the
// standard library fails to, elsewhere anet calls the standard library.
func readInterfaces() ([]localInterface, error) {
	ifaces, err := anet.Interfaces()
	if err != nil {
		return nil, err
	}

	list := make([]localInterface, 0, len(ifaces))
	for i := range ifaces {
		addrs, err := anet.InterfaceAddrsByInterface(&ifaces[i])
		if err != nil {
			// it may have gone since the list was read
			continue
		}
		iface := localInterface{name: ifaces[i].Name, flags: ifaces[i].Flags}
		for _, addr := range addrs {
			// a *net.IPNet prints as 192.168.1.5/24
			if prefix, err := netip.ParsePrefix(addr.String()); err == nil {
				iface.prefixes = append(iface.prefixes, prefix)
			}
		}
		list = append(list, iface)
	}

	return list, nil
}

// announced is the libp2p AddrsFactory: it takes the addresses of the host and
// returns the ones to tell other peers about in identify.
//
// On Android it adds the addresses of our network interfaces, see
// resolveListenAddrs. libp2p drops the duplicates after the factory.
//
// Then it drops the addresses no other peer can reach us at: our own addresses
// on the awl interface, addresses on interfaces that are not running, such as a
// bridge with nothing connected or a port with the cable out, and addresses on
// docker bridges. An address that is on none of the interfaces is kept:
// observed and port-mapped public addresses are not on any. Relay addresses are
// kept as well.
func (l *localAddrs) announced(hostAddrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	ifaces := l.interfaces()
	addrs := append(slices.Clone(hostAddrs), l.resolveListenAddrs(ifaces)...)

	ifaceByIP := make(map[netip.Addr]localInterface)
	for _, iface := range ifaces {
		for _, prefix := range iface.prefixes {
			ifaceByIP[prefix.Addr()] = iface
		}
	}

	return slices.DeleteFunc(addrs, func(addr multiaddr.Multiaddr) bool {
		if _, err := addr.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
			return false
		}
		netIP, err := manet.ToIP(addr)
		if err != nil {
			return false
		}
		ip, _ := netip.AddrFromSlice(netIP)
		ip = ip.Unmap()
		if slices.ContainsFunc(l.vpnPrefixes, func(prefix netip.Prefix) bool { return prefix.Addr() == ip }) {
			return true
		}
		iface, found := ifaceByIP[ip]
		return found && (iface.flags&net.FlagRunning == 0 || dockerBridgeName.MatchString(iface.name))
	})
}

// resolveListenAddrs turns every unspecified listen address, such as
// /ip4/0.0.0.0/udp/4363/quic-v1, into one address per IP of ifaces of the same
// family. libp2p does the same with the interfaces it lists itself, but on
// Android 11+ it gets only loopback (go-libp2p#2986), so the phone would never
// announce its LAN address. The factory receives the addresses already
// resolved, hence the listen addresses. Drop this once libp2p lists the
// interfaces on Android.
func (l *localAddrs) resolveListenAddrs(ifaces []localInterface) []multiaddr.Multiaddr {
	listenAddrs := l.listenAddrs.Load()
	if listenAddrs == nil {
		// not Android, or libp2p.New is still running
		return nil
	}

	var ifaceAddrs []multiaddr.Multiaddr
	for _, iface := range ifaces {
		for _, prefix := range iface.prefixes {
			ip := prefix.Addr()
			// libp2p drops them as well
			if ip.Is6() && ip.IsLinkLocalUnicast() {
				continue
			}
			if addr, err := manet.FromIP(ip.AsSlice()); err == nil {
				ifaceAddrs = append(ifaceAddrs, addr)
			}
		}
	}

	var resolved []multiaddr.Multiaddr
	for _, listenAddr := range (*listenAddrs)() {
		// Not ResolveUnspecifiedAddresses: with no interface addresses it lists
		// them with the standard library, which is what fails on Android.
		if !manet.IsIPUnspecified(listenAddr) {
			continue
		}
		// fails when no interface has an address of this family
		if addrs, err := manet.ResolveUnspecifiedAddress(listenAddr, ifaceAddrs); err == nil {
			resolved = append(resolved, addrs...)
		}
	}

	return resolved
}
