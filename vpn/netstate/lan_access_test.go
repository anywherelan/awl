package netstate

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeLANPeers(t *testing.T) {
	addrs := func(ss ...string) []netip.Addr {
		res := make([]netip.Addr, 0, len(ss))
		for _, s := range ss {
			res = append(res, netip.MustParseAddr(s))
		}
		return res
	}

	require.Empty(t, normalizeLANPeers(nil))
	require.Equal(t,
		addrs("10.66.0.2", "10.66.0.10", "fd00:66::2"),
		normalizeLANPeers(append(addrs("fd00:66::2", "10.66.0.10", "::ffff:10.66.0.2", "10.66.0.10"), netip.Addr{})),
	)

	peers := addrs("10.66.0.2", "10.66.0.10", "fd00:66::2", "fd00:66::3")
	require.Equal(t, addrs("10.66.0.2", "10.66.0.10"), lanPeersOf(peers, false))
	require.Equal(t, addrs("fd00:66::2", "fd00:66::3"), lanPeersOf(peers, true))
	require.Empty(t, lanPeersOf(addrs("10.66.0.2"), true))
	require.Len(t, peers, 4, "lanPeersOf must not modify its argument")
}
