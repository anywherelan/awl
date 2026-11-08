package p2p

import (
	"net"
	"net/netip"
	"testing"

	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestLocalAddrsAnnounced(t *testing.T) {
	const running = net.FlagUp | net.FlagRunning
	iface := func(name string, flags net.Flags, prefixes ...string) localInterface {
		li := localInterface{name: name, flags: flags}
		for _, p := range prefixes {
			li.prefixes = append(li.prefixes, netip.MustParsePrefix(p))
		}
		return li
	}
	desktop := []localInterface{
		iface("lo", running|net.FlagLoopback, "127.0.0.1/8"),
		iface("wlan0", running, "192.168.1.5/24"),
		// a LAN that overlaps the awl subnet
		iface("eth0", running, "10.66.5.7/16"),
		iface("awl0", running|net.FlagPointToPoint, "10.66.0.1/16", "fd00:66::1/48"),
		iface("docker0", net.FlagUp, "172.17.0.1/16"),
		iface("br-4ff02d8bfb49", running, "172.18.0.1/16"),
		// a bridge with the real LAN, as on a server with VMs
		iface("br0", running, "192.168.10.2/24"),
		iface("virbr0", net.FlagUp, "192.168.122.1/24"),
	}
	// what anet lists on Android, where libp2p sees loopback only
	android := []localInterface{
		iface("lo", running|net.FlagLoopback, "127.0.0.1/8", "::1/128"),
		iface("wlan0", running, "192.168.1.5/24", "fe80::1/64"),
		// VpnService TUN with our awl address
		iface("tun0", running|net.FlagPointToPoint, "10.66.0.1/16"),
		iface("rmnet_data0", running, "10.200.3.4/30"),
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
			ifaces: desktop,
			addrs:  []string{quic("10.66.0.1"), "/ip6/fd00:66::1/udp/4363/quic-v1", quic("10.66.5.7"), quic("192.168.1.5")},
			want:   []string{quic("10.66.5.7"), quic("192.168.1.5")},
		},
		{
			name:   "unreachable interfaces",
			ifaces: desktop,
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
				vpnPrefixes: []netip.Prefix{
					netip.MustParsePrefix("10.66.0.1/16"),
					netip.MustParsePrefix("fd00:66::1/48"),
				},
				read: func() ([]localInterface, error) { return tt.ifaces, nil },
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
