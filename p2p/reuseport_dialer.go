package p2p

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"syscall"

	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	"github.com/libp2p/go-reuseport"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// TCPDialStats counts successful outbound TCP dials of the main libp2p host
// by the source port they left from. Only maintained when socket marking is
// on (see reuseportDialer); zero otherwise.
type TCPDialStats struct {
	// FromListenPort is what hole punching needs. If it stays at zero while
	// FromRandomPort grows, port reuse is silently broken.
	FromListenPort int64
	FromRandomPort int64
}

// reuseportDialer dials TCP from the listen port of its own swarm while still
// applying the socket-marking control func.
//
// go-libp2p's tcp.WithDialerForAddr replaces the stock dialer entirely,
// including its reuseport logic. Without the source port being the listen
// port, identify observations are discarded (the local address of the
// connection is not a listen address), so a node behind NAT never learns its
// external TCP address and TCP hole punching cannot work. This dialer
// re-implements the stock source port selection (p2p/net/reuseport in
// go-libp2p) and chains reuseport.Control with the marking control func.
//
// One dialer per TCP transport instance: go-libp2p builds extra hosts from
// the same transport options (the AutoNAT dial-back hosts). Those have no
// listeners, so their dialers find nothing to reuse and dial from a
// random port — which is what AutoNAT relies on.
//
// Last compared against go-libp2p v0.50.0 (p2p/net/reuseport: dialer.go,
// reuseport.go). Being a copy, it has to be re-compared on every go-libp2p
// update; TestReuseportDialer_SyncedWithLibp2pVersion fails until that is
// done.
type reuseportDialer struct {
	control     func(network, address string, c syscall.RawConn) error
	listenAddrs func() []multiaddr.Multiaddr
	sw          *swarm.Swarm

	fromListenPort atomic.Int64
	fromRandomPort atomic.Int64
}

func (d *reuseportDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if laddr := d.localAddr(network, address); laddr != nil {
		dialer := net.Dialer{LocalAddr: laddr, Control: d.reuseControl}
		conn, err := dialer.DialContext(ctx, network, address)
		if err == nil {
			d.fromListenPort.Add(1)
			return conn, nil
		}
		if !shouldRetryWithoutReuse(err) || ctx.Err() != nil {
			return nil, err
		}
		// Most likely the 4-tuple is taken: we already have a connection
		// to this address from the listen port, or one is in TIME-WAIT.
	}

	dialer := net.Dialer{Control: d.control}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	d.fromRandomPort.Add(1)
	return conn, nil
}

func (d *reuseportDialer) stats() TCPDialStats {
	return TCPDialStats{
		FromListenPort: d.fromListenPort.Load(),
		FromRandomPort: d.fromRandomPort.Load(),
	}
}

func (d *reuseportDialer) reuseControl(network, address string, c syscall.RawConn) error {
	if err := reuseport.Control(network, address, c); err != nil {
		return err
	}
	return d.control(network, address, c)
}

// localAddr picks the listener whose port the dial should leave from, or nil
// when there is none. network is "tcp4" or "tcp6" and address is a literal
// IP:port — that is what the TCP transport passes to a custom dialer.
//
// Unlike the stock dialer, listeners bound to a specific non-loopback IP are
// not reused: that needs a route lookup per dial, and awl listens on the
// unspecified address by default.
func (d *reuseportDialer) localAddr(network, address string) *net.TCPAddr {
	if !tcpreuse.ReuseportIsAvailable() {
		return nil
	}
	remote, err := netip.ParseAddrPort(address)
	if err != nil {
		return nil
	}
	wantV4 := network == "tcp4"
	if !wantV4 && network != "tcp6" {
		return nil
	}

	var unspecified *net.TCPAddr
	for _, maddr := range d.listenAddrs() {
		laddr := tcpListenAddr(maddr)
		if laddr == nil || (laddr.IP.To4() != nil) != wantV4 {
			continue
		}
		switch {
		case laddr.IP.IsLoopback():
			if remote.Addr().IsLoopback() {
				return laddr
			}
		case laddr.IP.IsUnspecified():
			if unspecified == nil {
				unspecified = laddr
			}
		}
	}
	return unspecified
}

// tcpListenAddr returns the TCP address of a plain /ip*/tcp listen multiaddr,
// or nil for anything else (QUIC, circuit, websocket).
func tcpListenAddr(maddr multiaddr.Multiaddr) *net.TCPAddr {
	protocols := maddr.Protocols()
	if len(protocols) != 2 || protocols[1].Code != multiaddr.P_TCP {
		return nil
	}
	addr, err := manet.ToNetAddr(maddr)
	if err != nil {
		return nil
	}
	tcpAddr, _ := addr.(*net.TCPAddr)
	return tcpAddr
}

// shouldRetryWithoutReuse reports whether a failed reused-port dial is worth
// repeating from a random port: everything but a timeout is. This is what
// reuseErrShouldRetry in go-libp2p does in effect, and it is deliberately not
// narrowed to bind errors: a failure specific to the reused 4-tuple (a stale
// connection on the remote side or in a middlebox) may surface as any error,
// and the price of retrying a genuine one is a single extra dial.
func shouldRetryWithoutReuse(err error) bool {
	var netErr net.Error
	isTimeout := errors.As(err, &netErr) && netErr.Timeout()
	return !isTimeout
}
