// Package dnsbridge feeds IP packets intercepted from the VPN tunnel into a
// userspace TCP/IP stack (gVisor, via the wireguard-go netstack wrapper) that
// owns the in-tunnel DNS IP. It exists for platforms where awldns cannot bind
// an OS socket on :53 (Android: ports below 1024 need root) — a regular DNS
// server listens on dnsIP:53 (UDP and TCP) inside the stack through
// UDPConn/TCPListener instead.
//
// The bridge is deliberately decoupled from the rest of awl: packets come in
// through HandlePacket as raw bytes, responses leave through the writePacket
// callback, and the DNS listeners are plain net.PacketConn/net.Listener.
package dnsbridge

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/ipfs/go-log/v2"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/anywherelan/awl/metrics"
)

const (
	dnsPort = 53
	// closeDrainDelay is how long Close lets the response pump drain packets
	// already emitted by the stack before destroying it, see Close.
	closeDrainDelay = 100 * time.Millisecond
)

type Bridge struct {
	dnsIP       netip.Addr
	mtu         int
	dev         tun.Device
	udpConn     net.PacketConn
	tcpListener net.Listener
	writePacket func(packet []byte) error

	closed atomic.Bool
	logger *log.ZapEventLogger
}

// New creates a bridge owning dnsIP, with listeners on dnsIP:53. writePacket
// is called from a background goroutine with every IP packet the stack emits
// (DNS responses, TCP control segments); the callee must not retain the
// slice. mtu should match the VPN interface MTU.
func New(dnsIP netip.Addr, mtu int, writePacket func(packet []byte) error) (*Bridge, error) {
	dev, nsNet, err := netstack.CreateNetTUN([]netip.Addr{dnsIP}, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("create netstack: %v", err)
	}
	udpConn, err := nsNet.ListenUDPAddrPort(netip.AddrPortFrom(dnsIP, dnsPort))
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("netstack: listen udp on %s: %v", netip.AddrPortFrom(dnsIP, dnsPort), err)
	}
	tcpListener, err := nsNet.ListenTCPAddrPort(netip.AddrPortFrom(dnsIP, dnsPort))
	if err != nil {
		_ = udpConn.Close()
		_ = dev.Close()
		return nil, fmt.Errorf("netstack: listen tcp on %s: %v", netip.AddrPortFrom(dnsIP, dnsPort), err)
	}

	b := &Bridge{
		dnsIP:       dnsIP,
		mtu:         mtu,
		dev:         dev,
		udpConn:     udpConn,
		tcpListener: tcpListener,
		writePacket: writePacket,
		logger:      log.Logger("awl/dnsbridge"),
	}
	go b.readResponses()

	return b, nil
}

// DNSIP returns the in-tunnel IP the bridge answers on.
func (b *Bridge) DNSIP() netip.Addr {
	return b.dnsIP
}

// UDPConn is the bridge-side UDP listener on dnsIP:53, to be served by a DNS
// server (e.g. dns.Server{PacketConn: ...} with ActivateAndServe).
func (b *Bridge) UDPConn() net.PacketConn {
	return b.udpConn
}

// TCPListener is the bridge-side TCP listener on dnsIP:53.
func (b *Bridge) TCPListener() net.Listener {
	return b.tcpListener
}

// HandlePacket injects an IP packet addressed to the DNS IP into the stack.
// The packet data is copied — the caller may reuse its buffer immediately.
// Safe to call concurrently with Close (packets are dropped after close).
func (b *Bridge) HandlePacket(packet []byte) {
	if b.closed.Load() || len(packet) == 0 {
		return
	}
	metrics.DNSInterceptPacketsTotal.Inc()
	// Defensive clone: our caller (the TUN read loop) reuses its buffers, so
	// the packet must not be aliased past this call. netstack's Write currently
	// copies the data into its own storage (buffer.MakeWithData), so this clone
	// is technically redundant today — but we don't want correctness to depend
	// on that gVisor internal staying copy-on-write across upstream bumps.
	_, err := b.dev.Write([][]byte{bytes.Clone(packet)}, 0)
	if err != nil {
		metrics.DNSInterceptDroppedTotal.WithLabelValues("netstack_write").Inc()
		b.logger.Debugf("write packet to netstack: %v", err)
	}
}

// Close shuts the bridge down: the listeners stop accepting, the stack is
// destroyed and the response-reading goroutine exits. Idempotent. The DNS
// servers running on the listeners should be shut down by their owner first;
// closing the listeners here is a safety net.
func (b *Bridge) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	_ = b.udpConn.Close()
	_ = b.tcpListener.Close()
	// Upstream race in wireguard-go netstack: the stack delivers outgoing
	// packets via a blocking send to an unbuffered channel (netTun.WriteNotify),
	// and netTun.Close closes that channel without waiting for in-flight sends —
	// a send racing the close panics the process. Closing the stack itself
	// aborts live TCP endpoints, which emits RSTs into exactly that path. The
	// closed flag (no new injected packets) plus the closed listeners stop new
	// traffic, and keeping the pump draining for a moment lets already-emitted
	// packets through — shrinking the window to almost nothing, though not to
	// zero.
	time.Sleep(closeDrainDelay)
	// Unblocks the pending Read in readResponses.
	_ = b.dev.Close()
}

// readResponses pumps packets emitted by the stack into writePacket until the
// bridge is closed.
func (b *Bridge) readResponses() {
	// netstack's BatchSize is 1: single buffer, one packet per Read.
	bufs := [][]byte{make([]byte, b.mtu)}
	sizes := make([]int, 1)
	for {
		n, err := b.dev.Read(bufs, sizes, 0)
		if err != nil {
			// os.ErrClosed is the normal shutdown path.
			if b.closed.Load() || errors.Is(err, os.ErrClosed) {
				return
			}
			// A transient per-packet error must not kill the pump: the bridge
			// would still look alive while device DNS silently went dead.
			b.logger.Errorf("read from netstack: %v", err)
			continue
		}
		for i := 0; i < n; i++ {
			if sizes[i] == 0 {
				continue
			}
			writeErr := b.writePacket(bufs[i][:sizes[i]])
			if writeErr != nil {
				metrics.DNSInterceptDroppedTotal.WithLabelValues("tun_write").Inc()
				b.logger.Debugf("write response packet: %v", writeErr)
			}
		}
	}
}
