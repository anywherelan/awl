package awldns

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// testNames and testNamesV6 are what newTestResolver serves. As in
// config.DNSNamesMapping, a peer has a peer ID name and a domain name.
var (
	testNames = map[string]netip.Addr{
		"laptop":         netip.MustParseAddr("10.66.0.2"),
		"laptop_peer_id": netip.MustParseAddr("10.66.0.2"),
		"pc.office":      netip.MustParseAddr("10.66.0.3"),
		"pc_peer_id":     netip.MustParseAddr("10.66.0.3"),
	}
	testNamesV6 = map[string]netip.Addr{
		"pc.office":  netip.MustParseAddr("fd00:66::3"),
		"pc_peer_id": netip.MustParseAddr("fd00:66::3"),
		"v6only":     netip.MustParseAddr("fd00:66::4"),
	}
)

func TestDNS(t *testing.T) {
	addr := newTestResolver(t)

	cases := []struct {
		name  string
		query string
		qtype uint16
		rcode int
		want  []string // answers: addresses or PTR names
	}{
		{"A", "laptop.awl.", dns.TypeA, dns.RcodeSuccess, []string{"10.66.0.2"}},
		{"AByPeerID", "laptop_peer_id.awl.", dns.TypeA, dns.RcodeSuccess, []string{"10.66.0.2"}},
		{"ACaseInsensitive", "LapTop.AWL.", dns.TypeA, dns.RcodeSuccess, []string{"10.66.0.2"}},
		{"ADottedName", "pc.office.awl.", dns.TypeA, dns.RcodeSuccess, []string{"10.66.0.3"}},
		{"AAAA", "pc.office.awl.", dns.TypeAAAA, dns.RcodeSuccess, []string{"fd00:66::3"}},
		{"ANY", "pc.office.awl.", dns.TypeANY, dns.RcodeSuccess, []string{"10.66.0.3", "fd00:66::3"}},
		{"AAAAWithoutIPv6", "laptop.awl.", dns.TypeAAAA, dns.RcodeSuccess, nil},
		{"AWithoutIPv4", "v6only.awl.", dns.TypeA, dns.RcodeSuccess, nil},
		{"AAAAWithoutIPv4", "v6only.awl.", dns.TypeAAAA, dns.RcodeSuccess, []string{"fd00:66::4"}},
		{"UnknownA", "unknown.awl.", dns.TypeA, dns.RcodeNameError, nil},
		{"UnknownAAAA", "unknown.awl.", dns.TypeAAAA, dns.RcodeNameError, nil},
		{"UnknownANY", "unknown.awl.", dns.TypeANY, dns.RcodeNameError, nil},
		// the shortest of the names of an address
		{"PTR", reverseName(t, "10.66.0.2"), dns.TypePTR, dns.RcodeSuccess, []string{"laptop.awl."}},
		{"PTRIPv6", reverseName(t, "fd00:66::3"), dns.TypePTR, dns.RcodeSuccess, []string{"pc.office.awl."}},
		// unknown addresses go to the upstream, which the test resolver has none of
		{"PTRUnknown", reverseName(t, "10.66.0.99"), dns.TypePTR, dns.RcodeServerFailure, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := exchange(t, "udp", addr, tc.query, tc.qtype)
			require.Equal(t, tc.rcode, resp.Rcode, dns.RcodeToString[resp.Rcode])

			var got []string
			for _, rr := range resp.Answer {
				// some clients expect the name exactly as they asked
				require.Equal(t, tc.query, rr.Header().Name)
				switch rr := rr.(type) {
				case *dns.A:
					got = append(got, rr.A.String())
				case *dns.AAAA:
					got = append(got, rr.AAAA.String())
				case *dns.PTR:
					got = append(got, rr.Ptr)
				}
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestPTRNameToAddr(t *testing.T) {
	v6 := reverseName(t, "fd00:66::abc")
	cases := []struct {
		name string
		want string // empty: malformed
	}{
		{"2.0.66.10.in-addr.arpa.", "10.66.0.2"},
		{"2.0.66.10.IN-ADDR.ARPA.", "10.66.0.2"},
		{"0.66.10.in-addr.arpa.", ""},
		{"x.0.66.10.in-addr.arpa.", ""},
		{v6, "fd00:66::abc"},
		{strings.ToUpper(v6), "fd00:66::abc"},
		{"c.b.a.ip6.arpa.", ""},
		{"cc" + strings.TrimPrefix(v6, "c"), ""}, // 32 labels, but one has two digits
		{"g" + strings.TrimPrefix(v6, "c"), ""},
	}
	for _, tc := range cases {
		got := ptrNameToAddr(tc.name)
		if tc.want == "" {
			require.False(t, got.IsValid(), "%s: got %s", tc.name, got)
			continue
		}
		require.Equal(t, tc.want, got.String(), tc.name)
	}
}

func TestResolverFromListeners(t *testing.T) {
	a := require.New(t)

	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	a.NoError(err)
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	a.NoError(err)

	// The reported address is decoupled from the listeners — on Android it is
	// the in-tunnel DNS IP, while the listeners live inside netstack.
	const dnsAddress = "10.66.255.254:53"
	resolver := NewResolverFromListeners(udpConn, tcpListener, dnsAddress)
	defer resolver.Close()

	resolver.ReceiveConfiguration("", testNames, nil)

	// DNSAddress reports the passed address once both servers are serving.
	a.Eventually(func() bool { return resolver.DNSAddress() == dnsAddress },
		5*time.Second, 10*time.Millisecond)

	for network, addr := range map[string]string{
		"udp": udpConn.LocalAddr().String(),
		"tcp": tcpListener.Addr().String(),
	} {
		resp := exchange(t, network, addr, "laptop.awl.", dns.TypeA)
		a.Len(resp.Answer, 1, network)
		a.Equal("10.66.0.2", resp.Answer[0].(*dns.A).A.String(), network)
	}

	resolver.Close()
	a.Eventually(func() bool { return resolver.DNSAddress() == "" },
		5*time.Second, 10*time.Millisecond)
}

// newTestResolver starts a resolver serving testNames and testNamesV6 on a
// free local port and returns its address once it serves.
func newTestResolver(t *testing.T) string {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", findFreePort())
	resolver := NewResolver(addr)
	t.Cleanup(resolver.Close)
	resolver.ReceiveConfiguration("", testNames, testNamesV6)

	require.Eventually(t, func() bool { return resolver.DNSAddress() != "" },
		5*time.Second, 10*time.Millisecond)
	return addr
}

// exchange sends a query for name to the DNS server at addr over network.
func exchange(t *testing.T, network, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(name, qtype)
	resp, _, err := (&dns.Client{Net: network}).Exchange(req, addr)
	require.NoError(t, err)
	return resp
}

// reverseName returns the PTR query name of addr.
func reverseName(t *testing.T, addr string) string {
	t.Helper()
	name, err := dns.ReverseAddr(addr)
	require.NoError(t, err)
	return name
}

func findFreePort() int {
	const maxAttempts = 50
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port

		// The DNS resolver listens on both TCP and UDP on this port, so it must be
		// free for both. A TCP-free port is not guaranteed to be UDP-free, and on
		// Windows the chosen port may fall inside an OS-excluded range (Hyper-V/WSL
		// reservations), which fails the UDP bind with WSAEACCES. Release the port
		// and try another instead of giving up.
		u, err := net.ListenPacket("udp", l.Addr().String())
		if err != nil {
			_ = l.Close()
			lastErr = err
			continue
		}
		_ = u.Close()
		_ = l.Close()
		return port
	}
	panic(fmt.Sprintf("failed to find a free tcp+udp port after %d attempts: %v", maxAttempts, lastErr))
}
