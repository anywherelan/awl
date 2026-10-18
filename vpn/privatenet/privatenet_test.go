package privatenet

import (
	"net/netip"
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

func TestContains(t *testing.T) {
	for _, addr := range []string{"10.66.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1",
		"169.254.169.254", "::ffff:192.168.1.1", "fd00::1", "fe80::1"} {
		require.True(t, Contains(netip.MustParseAddr(addr)), addr)
	}
	for _, addr := range []string{"8.8.8.8", "172.32.0.1", "127.0.0.1", "2a00:1450::1", "::ffff:8.8.8.8"} {
		require.False(t, Contains(netip.MustParseAddr(addr)), addr)
	}
}
