package p2p

import (
	"net"
	"net/netip"
	"testing"

	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

const testRunning = net.FlagUp | net.FlagRunning

func testIface(name string, flags net.Flags, prefixes ...string) localInterface {
	li := localInterface{name: name, flags: flags}
	for _, p := range prefixes {
		li.prefixes = append(li.prefixes, netip.MustParsePrefix(p))
	}
	return li
}

// testDesktopIfaces are the network interfaces of a desktop with docker and VMs.
var testDesktopIfaces = []localInterface{
	testIface("lo", testRunning|net.FlagLoopback, "127.0.0.1/8", "::1/128"),
	testIface("wlan0", testRunning, "192.168.1.5/24", "fe80::1/64", "fd12:3456::5/64"),
	// a LAN inside the awl subnet
	testIface("eth0", testRunning, "10.66.5.7/16"),
	// a work VPN with all of 10.0.0.0/8, wider than the awl subnet
	testIface("tun1", testRunning|net.FlagPointToPoint, "10.8.0.2/8"),
	// no answer from DHCP
	testIface("eth1", testRunning, "169.254.7.7/16"),
	// a public address on the interface, as on a server
	testIface("eth2", testRunning, "89.36.211.215/24"),
	testIface("awl0", testRunning|net.FlagPointToPoint, "10.66.0.1/11", "fd00:66::1/48"),
	testIface("docker0", net.FlagUp, "172.17.0.1/16"),
	testIface("br-4ff02d8bfb49", testRunning, "172.18.0.1/16"),
	// a bridge with the real LAN, as on a server with VMs
	testIface("br0", testRunning, "192.168.10.2/24"),
	testIface("virbr0", net.FlagUp, "192.168.122.1/24"),
}

// testVPNPrefixes are our addresses on awl0 of testDesktopIfaces.
var testVPNPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.66.0.1/11"),
	netip.MustParsePrefix("fd00:66::1/48"),
}

func TestLocalAddrsAnnounced(t *testing.T) {
	// what anet lists on Android, where libp2p sees loopback only
	android := []localInterface{
		testIface("lo", testRunning|net.FlagLoopback, "127.0.0.1/8", "::1/128"),
		testIface("wlan0", testRunning, "192.168.1.5/24", "fe80::1/64"),
		// VpnService TUN with our awl address
		testIface("tun0", testRunning|net.FlagPointToPoint, "10.66.0.1/11"),
		testIface("rmnet_data0", testRunning, "10.200.3.4/30"),
	}

	quic := func(ip string) string { return "/ip4/" + ip + "/udp/4363/quic-v1" }
	const relay = "/ip4/127.0.0.1/udp/4001/quic-v1/p2p/12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ/p2p-circuit"

	tests := []struct {
		name   string
		ifaces []localInterface
		listen []string
		addrs  []string
		want   []string
	}{
		{
			name:   "our awl addresses",
			ifaces: testDesktopIfaces,
			addrs:  []string{quic("10.66.0.1"), "/ip6/fd00:66::1/udp/4363/quic-v1", quic("10.66.5.7"), quic("192.168.1.5")},
			want:   []string{quic("10.66.5.7"), quic("192.168.1.5")},
		},
		{
			name:   "unreachable interfaces",
			ifaces: testDesktopIfaces,
			addrs: []string{
				quic("127.0.0.1"), quic("192.168.1.5"),
				quic("192.168.122.1"), quic("172.17.0.1"), quic("172.18.0.1"),
				quic("192.168.10.2"),
				// observed, on no interface
				quic("203.0.113.7"),
				relay,
			},
			want: []string{quic("127.0.0.1"), quic("192.168.1.5"), quic("192.168.10.2"), quic("203.0.113.7"), relay},
		},
		{
			name:   "Android: LAN addresses libp2p did not list",
			ifaces: android,
			listen: []string{quic("0.0.0.0"), "/ip6/::/udp/4363/quic-v1", "/p2p-circuit"},
			addrs:  []string{quic("127.0.0.1"), quic("203.0.113.7")},
			want: []string{
				quic("127.0.0.1"), "/ip6/::1/udp/4363/quic-v1", quic("203.0.113.7"),
				quic("192.168.1.5"), quic("10.200.3.4"),
			},
		},
	}
	toMultiaddrs := func(strs []string) []multiaddr.Multiaddr {
		addrs := make([]multiaddr.Multiaddr, 0, len(strs))
		for _, s := range strs {
			addrs = append(addrs, multiaddr.StringCast(s))
		}
		return addrs
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &localAddrs{
				vpnPrefixes: testVPNPrefixes,
				read:        func() ([]localInterface, error) { return tt.ifaces, nil },
			}
			listen := func() []multiaddr.Multiaddr { return toMultiaddrs(tt.listen) }
			l.listenAddrs.Store(&listen)

			// libp2p drops the duplicates after the factory
			got := multiaddr.Unique(l.announced(toMultiaddrs(tt.addrs)))

			gotStrings := make([]string, 0, len(got))
			for _, a := range got {
				gotStrings = append(gotStrings, a.String())
			}
			require.ElementsMatch(t, tt.want, gotStrings)
		})
	}
}

func TestLocalAddrsLANCandidates(t *testing.T) {
	l := &localAddrs{
		vpnPrefixes: testVPNPrefixes,
		read:        func() ([]localInterface, error) { return testDesktopIfaces, nil },
	}
	quic := func(ip string) string { return "/ip4/" + ip + "/udp/4363/quic-v1" }

	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "LAN", addr: quic("192.168.1.7"), want: true},
		{name: "LAN over TCP", addr: "/ip4/192.168.1.7/tcp/4363", want: true},
		{name: "LAN on a bridge", addr: quic("192.168.10.9"), want: true},
		{name: "IPv6 ULA of the LAN", addr: "/ip6/fd12:3456::7/udp/4363/quic-v1", want: true},
		{name: "loopback", addr: quic("127.0.0.1")},
		{name: "IPv4 link-local", addr: quic("169.254.3.4")},
		{name: "IPv6 link-local", addr: "/ip6/fe80::7/udp/4363/quic-v1"},
		{name: "LAN inside the awl subnet", addr: quic("10.66.5.9"), want: true},
		{name: "awl subnet inside a wider network", addr: quic("10.90.0.2")},
		{name: "awl IPv6 subnet", addr: "/ip6/fd00:66::2/udp/4363/quic-v1"},
		{name: "docker", addr: quic("172.18.0.2")},
		{name: "interface that is not running", addr: quic("192.168.122.5")},
		{name: "private, not in our subnets", addr: quic("192.168.2.7")},
		{name: "public, in our subnet", addr: quic("89.36.211.9")},
		{
			name: "relay in the LAN",
			addr: quic("192.168.1.1") + "/p2p/12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ/p2p-circuit",
		},
		{name: "DNS", addr: "/dns4/nas.local/udp/4363/quic-v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.lanCandidates([]multiaddr.Multiaddr{multiaddr.StringCast(tt.addr)})
			require.Equal(t, tt.want, len(got) == 1)
		})
	}
}
