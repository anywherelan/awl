package p2p

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-reuseport"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"
)

// These tests pin the go-libp2p behaviour reuseportDialer relies on. When one
// fails after a dependency update, outbound TCP has most likely fallen back
// to random source ports — nothing else breaks, but TCP hole punching
// silently stops working. See plans/tcp-hole-punching.md §8.1.

// countingControl stands in for the socket-marking control func, which needs
// root on Linux.
type countingControl struct {
	calls atomic.Int64
}

func (c *countingControl) control(_, _ string, _ syscall.RawConn) error {
	c.calls.Add(1)
	return nil
}

// syncedLibp2pVersion is the go-libp2p release reuseportDialer was last
// compared against. Bump it only after diffing p2p/net/reuseport (dialer.go,
// reuseport.go) between the two releases and porting what changed.
const syncedLibp2pVersion = "v0.50.0"

// reuseportDialer is a copy of go-libp2p's source port selection and retry
// rule, so it does not pick up upstream changes on its own. The tests below
// catch breakage but not drift; this one fails on every go-libp2p update to
// force the comparison.
func TestReuseportDialer_SyncedWithLibp2pVersion(t *testing.T) {
	// Test binaries carry no dependency list in their build info, so ask
	// the go command. A replaced module reports the replacement's version.
	out, err := exec.Command("go", "list", "-m", "-f",
		"{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}",
		"github.com/libp2p/go-libp2p").Output()
	require.NoError(t, err)

	require.Equal(t, syncedLibp2pVersion, strings.TrimSpace(string(out)),
		"go-libp2p changed: compare p2p/net/reuseport with p2p/reuseport_dialer.go, then update syncedLibp2pVersion")
}

func TestReuseportDialer_HostDialsFromListenPort(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		dialIP     string
	}{
		{name: "ip4 unspecified listener", listenAddr: "/ip4/0.0.0.0/tcp/0", dialIP: "/ip4/127.0.0.1"},
		{name: "ip4 loopback listener", listenAddr: "/ip4/127.0.0.1/tcp/0", dialIP: "/ip4/127.0.0.1"},
		{name: "ip6 unspecified listener", listenAddr: "/ip6/::/tcp/0", dialIP: "/ip6/::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &countingControl{}
			a := newTestP2p(t, ctrl.control, tt.listenAddr)
			b := newTestP2p(t, ctrl.control, tt.listenAddr)

			conn := connectTCP(t, a, b, tt.dialIP)

			require.Equal(t, tcpListenPort(t, a), tcpPort(t, conn.LocalMultiaddr()),
				"outbound TCP must leave from the listen port, otherwise identify observations are discarded")
			require.Equal(t, TCPDialStats{FromListenPort: 1}, a.TCPDialStats())
			require.Positive(t, ctrl.calls.Load(), "the marking control func must run on the reused-port socket")
		})
	}
}

// The stock path (no control func: macOS, bootstrap nodes) must reuse the
// port on its own.
func TestReuseportDialer_StockTransportDialsFromListenPort(t *testing.T) {
	a := newTestP2p(t, nil, "/ip4/0.0.0.0/tcp/0")
	b := newTestP2p(t, nil, "/ip4/0.0.0.0/tcp/0")

	conn := connectTCP(t, a, b, "/ip4/127.0.0.1")

	require.Equal(t, tcpListenPort(t, a), tcpPort(t, conn.LocalMultiaddr()))
	require.Empty(t, a.tcpDialers, "no custom dialer without a control func")
	require.Equal(t, TCPDialStats{}, a.TCPDialStats())
}

// go-libp2p builds the AutoNAT dial-back host from the same transport
// options. Its dialer must not reuse the main host's listen port: a dial-back
// from that port would hit the NAT mapping the probed peer already has to us.
func TestReuseportDialer_AutoNATHostDoesNotReusePort(t *testing.T) {
	ctrl := &countingControl{}
	a := newTestP2p(t, ctrl.control, "/ip4/0.0.0.0/tcp/0")

	// Exactly two: the main host and the AutoNATv2 dialer host. A different
	// number means go-libp2p changed how it builds its helper hosts —
	// re-read config/config.go there before adjusting this.
	require.Len(t, a.tcpDialers, 2)
	var helper *reuseportDialer
	for _, d := range a.tcpDialers {
		if network.Network(d.sw) != a.Host().Network() {
			helper = d
		}
	}
	require.NotNil(t, helper)

	target := listenReuseport(t)
	require.Nil(t, helper.localAddr("tcp4", target.Addr().String()), "the AutoNAT dialer host has no listeners to reuse")

	conn, err := helper.DialContext(context.Background(), "tcp4", target.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	require.NotEqual(t, tcpListenPort(t, a), conn.LocalAddr().(*net.TCPAddr).Port)
	require.Equal(t, TCPDialStats{FromRandomPort: 1}, helper.stats())
	require.Positive(t, ctrl.calls.Load(), "dial-backs must still be marked")
	require.Equal(t, TCPDialStats{}, a.TCPDialStats(), "helper host dials are not the main host's")
}

// A second connection to the same remote address cannot share the 4-tuple:
// the dial must fall back to a random port instead of failing. Not
// reachable through a host: the swarm never dials a connected peer again.
func TestReuseportDialer_FallsBackWhenTupleIsTaken(t *testing.T) {
	ctrl := &countingControl{}
	local := listenReuseport(t)
	target := listenReuseport(t)
	localMaddr, err := manet.FromNetAddr(local.Addr())
	require.NoError(t, err)
	dialer := &reuseportDialer{
		control:     ctrl.control,
		listenAddrs: func() []multiaddr.Multiaddr { return []multiaddr.Multiaddr{localMaddr} },
	}

	first, err := dialer.DialContext(context.Background(), "tcp4", target.Addr().String())
	require.NoError(t, err)
	defer first.Close()
	require.Equal(t, local.Addr().(*net.TCPAddr).Port, first.LocalAddr().(*net.TCPAddr).Port)
	callsAfterFirst := ctrl.calls.Load()

	second, err := dialer.DialContext(context.Background(), "tcp4", target.Addr().String())
	require.NoError(t, err)
	defer second.Close()
	require.NotEqual(t, local.Addr().(*net.TCPAddr).Port, second.LocalAddr().(*net.TCPAddr).Port)

	require.Equal(t, TCPDialStats{FromListenPort: 1, FromRandomPort: 1}, dialer.stats())
	require.Greater(t, ctrl.calls.Load(), callsAfterFirst, "the fallback socket must be marked too")
}

func TestShouldRetryWithoutReuse(t *testing.T) {
	require.True(t, shouldRetryWithoutReuse(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EADDRNOTAVAIL)}))
	require.True(t, shouldRetryWithoutReuse(errors.New("anything else")))
	require.False(t, shouldRetryWithoutReuse(&net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}))
	require.False(t, shouldRetryWithoutReuse(context.DeadlineExceeded))
}

func TestReuseportDialer_localAddr(t *testing.T) {
	tests := []struct {
		name    string
		listen  []string
		network string
		remote  string
		want    string
	}{
		{name: "unspecified ip4", listen: []string{"/ip4/0.0.0.0/tcp/4363"}, network: "tcp4", remote: "1.2.3.4:6150", want: "0.0.0.0:4363"},
		{name: "unspecified ip6", listen: []string{"/ip4/0.0.0.0/tcp/4363", "/ip6/::/tcp/4364"}, network: "tcp6", remote: "[2001:db8::1]:6150", want: "[::]:4364"},
		{name: "family mismatch", listen: []string{"/ip6/::/tcp/4363"}, network: "tcp4", remote: "1.2.3.4:6150"},
		{name: "quic listener only", listen: []string{"/ip4/0.0.0.0/udp/4363/quic-v1"}, network: "tcp4", remote: "1.2.3.4:6150"},
		{name: "loopback listener, loopback remote", listen: []string{"/ip4/127.0.0.1/tcp/4363"}, network: "tcp4", remote: "127.0.0.1:6150", want: "127.0.0.1:4363"},
		{name: "loopback listener, public remote", listen: []string{"/ip4/127.0.0.1/tcp/4363"}, network: "tcp4", remote: "1.2.3.4:6150"},
		{name: "specific listener is not reused", listen: []string{"/ip4/192.168.1.5/tcp/4363"}, network: "tcp4", remote: "1.2.3.4:6150"},
		{name: "no listeners", network: "tcp4", remote: "1.2.3.4:6150"},
		{name: "not an ip literal", listen: []string{"/ip4/0.0.0.0/tcp/4363"}, network: "tcp4", remote: "example.com:6150"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &reuseportDialer{listenAddrs: func() []multiaddr.Multiaddr {
				res := make([]multiaddr.Multiaddr, 0, len(tt.listen))
				for _, a := range tt.listen {
					res = append(res, multiaddr.StringCast(a))
				}
				return res
			}}
			got := d.localAddr(tt.network, tt.remote)
			if tt.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.want, got.String())
		})
	}
}

// newTestP2p builds a host through InitHost with the libp2p options the
// application passes in production, minus the ones that touch the network.
func newTestP2p(t *testing.T, control func(network, address string, c syscall.RawConn) error, listenAddr string) *P2p {
	t.Helper()
	peerstore, err := pstoremem.NewPeerstore()
	require.NoError(t, err)

	p := NewP2p(context.Background())
	_, err = p.InitHost(HostConfig{
		ListenAddrs:              []multiaddr.Multiaddr{multiaddr.StringCast(listenAddr)},
		AllowEmptyBootstrapPeers: true,
		SocketControlFunc:        control,
		Peerstore:                peerstore,
		DHTDatastore:             dssync.MutexWrap(ds.NewMapDatastore()),
		DHTOpts:                  []dht.Option{dht.DisableAutoRefresh()},
		Libp2pOpts: []libp2p.Option{
			libp2p.EnableRelay(),
			libp2p.EnableAutoNATv2(),
			libp2p.EnableHolePunching(),
		},
	})
	if err != nil && multiaddr.StringCast(listenAddr).Protocols()[0].Code == multiaddr.P_IP6 {
		t.Skipf("no IPv6 on this host: %v", err)
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// connectTCP dials b from a over loopback TCP and returns a's connection.
func connectTCP(t *testing.T, a, b *P2p, dialIP string) network.Conn {
	t.Helper()
	addr := multiaddr.StringCast(dialIP + "/tcp/" + strconv.Itoa(tcpListenPort(t, b)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, a.Host().Connect(ctx, peer.AddrInfo{ID: b.PeerID(), Addrs: []multiaddr.Multiaddr{addr}}))
	conns := a.Host().Network().ConnsToPeer(b.PeerID())
	require.Len(t, conns, 1)
	return conns[0]
}

func listenReuseport(t *testing.T) net.Listener {
	t.Helper()
	l, err := reuseport.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func tcpListenPort(t *testing.T, p *P2p) int {
	t.Helper()
	for _, maddr := range p.Host().Network().ListenAddresses() {
		if addr := tcpListenAddr(maddr); addr != nil {
			return addr.Port
		}
	}
	require.FailNow(t, "host has no TCP listener")
	return 0
}

func tcpPort(t *testing.T, maddr multiaddr.Multiaddr) int {
	t.Helper()
	addr := tcpListenAddr(maddr)
	require.NotNil(t, addr, "not a TCP multiaddr: %s", maddr)
	return addr.Port
}
