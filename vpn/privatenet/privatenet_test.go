package privatenet

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrefixes(t *testing.T) {
	for _, p := range PrefixesV4() {
		require.True(t, p.Addr().Is4(), "%s must be IPv4: the Windows gateway filter is IPv4-only", p)
		require.Equal(t, p.Masked(), p, "%s must be in canonical masked form", p)
	}
	for _, p := range PrefixesV6() {
		require.True(t, p.Addr().Is6(), "%s must be IPv6", p)
		require.Equal(t, p.Masked(), p, "%s must be in canonical masked form", p)
	}
}

func TestLANPrefixes(t *testing.T) {
	require.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("100.64.0.0/10"),
	}, LANPrefixesV4())
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("fc00::/7")}, LANPrefixesV6())

	// Every LAN range is a private range, and only link-local is left out.
	for _, c := range []struct {
		all, lan []netip.Prefix
	}{
		{PrefixesV4(), LANPrefixesV4()},
		{PrefixesV6(), LANPrefixesV6()},
	} {
		for _, p := range c.all {
			require.Equal(t, !p.Addr().IsLinkLocalUnicast(), slices.Contains(c.lan, p), p)
		}
		require.Len(t, c.all, len(c.lan)+1)
	}

	// The callers get copies.
	LANPrefixesV4()[0] = netip.MustParsePrefix("8.8.8.0/24")
	require.Equal(t, netip.MustParsePrefix("10.0.0.0/8"), LANPrefixesV4()[0])
}

func TestContains(t *testing.T) {
	for _, addr := range []string{"10.66.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1",
		"169.254.169.254", "::ffff:192.168.1.1", "fd00::1", "fe80::1"} {
		require.True(t, Contains(netip.MustParseAddr(addr)), addr)
	}
	for _, addr := range []string{"8.8.8.8", "172.32.0.1", "127.0.0.1", "2a00:1450::1", "::ffff:8.8.8.8"} {
		require.False(t, Contains(netip.MustParseAddr(addr)), addr)
	}
}
