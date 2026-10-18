package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/haxii/socks5"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/vpn/privatenet"
)

const (
	socks5Version = uint8(5)
)

type Server struct {
	socks        *socks5.Server
	conf         *socks5.Config
	rule         *UpdatableRule
	exitNodeRule *ExitNodeRule
}

// NewServer constructs the SOCKS5 exit-node server. dialControl, when non-nil,
// is applied to every outbound connection the server opens to the real
// destination — it is the same socket-marking callback used for libp2p
// sockets (sockmark). This keeps SOCKS5 exit-node traffic going out the host's
// physical NIC, independent of whether this node is itself a VPN gateway
// client (whose 0.0.0.0/0-via-TUN default route would otherwise capture these
// sockets). Marking unconditionally is safe: when gateway mode is off there is
// no matching fwmark ip-rule, so the kernel ignores the mark — mirroring how
// libp2p sockets are marked at all times (see application.go).
// A nil dialControl yields a plain context-aware dialer.
//
// awlPrefixes are the awl subnets, IPv4 and IPv6, which the exit node does not
// proxy to (see ExitNodeRule).
//
// TODO(socks5-udp): UDP ASSOCIATE is effectively unsupported today (we serve
// only libp2p streams via ServeConn; haxii's UDP relay starts only inside
// Serve(), and BindIP is a fake 127.0.0.1). If it is ever enabled, note that
// haxii creates its UDP sockets directly (net.ListenUDP / net.DialUDP in
// udp.go) — bypassing config.Dial and therefore this marking, on every
// platform. Those sockets would need their own dialControl-style hook,
// otherwise UDP relay traffic loops into the VPN gateway tunnel.
func NewServer(dialControl func(network, address string, c syscall.RawConn) error, awlPrefixes []netip.Prefix) *Server {
	exitNodeRule := &ExitNodeRule{awlPrefixes: awlPrefixes}
	rule := NewUpdatableRule(exitNodeRule)
	dialer := &net.Dialer{Control: dialControl}
	conf := &socks5.Config{
		// fake addr, we don't bind address for server
		BindIP:   net.IPv4(127, 0, 0, 1),
		BindPort: 8000,
		Rules:    rule,
		Logger:   NewLogger(),
		Resolver: exitNodeResolver{},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}
	server, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}

	return &Server{
		socks:        server,
		conf:         conf,
		rule:         rule,
		exitNodeRule: exitNodeRule,
	}
}

// SetRulesEnabled is for tests: with the rules off, the server proxies to any
// address, local ones included. Names in .awl are still refused.
func (s *Server) SetRulesEnabled(enabled bool) {
	if enabled {
		s.rule.SetRule(s.exitNodeRule)
	} else {
		s.rule.SetRule(socks5.PermitAll())
	}
}

func (s *Server) ServeStreamConn(stream network.Stream) error {
	conn := StreamConnWrapper{Stream: stream}
	return s.socks.ServeConn(conn)
}

func (s *Server) ServeStreamConnNoAuth(stream network.Stream) error {
	conn := StreamConnWrapper{Stream: stream}
	return s.socks.ServeConnNoAuth(conn)
}

// ServeConn is only used in tests. TODO: refactor tests
func (s *Server) ServeConn(ioConn io.ReadWriteCloser) error {
	conn := ReadWriterConnWrapper{ReadWriteCloser: ioConn}
	return s.socks.ServeConn(conn)
}

func (s *Server) SendServerFailureReply(conn io.ReadWriter) error {
	// https://datatracker.ietf.org/doc/html/rfc1928

	// Connect Request
	// +----+----------+----------+
	// |VER | NMETHODS | METHODS  |
	// +----+----------+----------+
	// | 1  |    1     | 1 to 255 |
	// +----+----------+----------+
	buf := make([]byte, 2)

	_, err := io.ReadFull(conn, buf)
	if err != nil {
		return err
	}
	buf = make([]byte, int(buf[1])) // Methods types
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		return err
	}

	// Connect Response
	// +----+--------+
	// |VER | METHOD |
	// +----+--------+
	// | 1  |   1    |
	// +----+--------+
	_, err = conn.Write([]byte{socks5Version, socks5.AuthMethodNoAuth})
	if err != nil {
		return err
	}

	// Read proxy request
	_, err = socks5.NewRequest(conn)
	if err != nil {
		return err
	}

	err = s.sendReply(conn, socks5.ReplyServerFailure, nil)
	if err != nil {
		return err
	}

	return nil
}

// sendReply is copied from github.com/haxii/socks5@v1.0.0/request.go:376
func (s *Server) sendReply(w io.Writer, resp uint8, addr *socks5.AddrSpec) error {
	// Format the address
	var addrType uint8
	var addrBody []byte
	var addrPort uint16
	switch {
	case addr == nil:
		addrType = socks5.AddressIPv4
		addrBody = []byte{0, 0, 0, 0}
		addrPort = 0

	case addr.FQDN != "":
		addrType = socks5.AddressDomainName
		addrBody = append([]byte{byte(len(addr.FQDN))}, addr.FQDN...)
		addrPort = uint16(addr.Port)

	case addr.IP.To4() != nil:
		addrType = socks5.AddressIPv4
		addrBody = []byte(addr.IP.To4())
		addrPort = uint16(addr.Port)

	case addr.IP.To16() != nil:
		addrType = socks5.AddressIPv6
		addrBody = []byte(addr.IP.To16())
		addrPort = uint16(addr.Port)

	default:
		return fmt.Errorf("failed to format address: %v", addr)
	}

	// Format the message
	msg := make([]byte, 6+len(addrBody))
	msg[0] = socks5Version
	msg[1] = resp
	msg[2] = 0 // Reserved
	msg[3] = addrType
	copy(msg[4:], addrBody)
	msg[4+len(addrBody)] = byte(addrPort >> 8)
	msg[4+len(addrBody)+1] = byte(addrPort & 0xff)

	// Send the message
	_, err := w.Write(msg)
	return err
}

type UpdatableRule struct {
	rule atomic.Pointer[socks5.RuleSet]
}

func NewUpdatableRule(rule socks5.RuleSet) *UpdatableRule {
	ur := &UpdatableRule{}
	ur.SetRule(rule)

	return ur
}

func (r *UpdatableRule) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	rule := *r.rule.Load()

	return rule.Allow(ctx, req)
}

func (r *UpdatableRule) SetRule(rule socks5.RuleSet) {
	r.rule.Store(&rule)
}

// ExitNodeRule keeps the exit node's own host and network from its peers. It
// refuses:
//   - loopback and the unspecified address (0.0.0.0, ::), which the OS also
//     connects to the local host;
//   - the LAN, CGNAT and link-local ranges (see privatenet), the latter
//     including the cloud metadata address 169.254.169.254;
//   - multicast;
//   - the awl subnets: the exit node's peers are not the client's.
//
// The address is checked after the name is resolved, and the connection goes
// to that same address, so a name resolving to a refused address is refused
// too. The exit node's own public addresses are allowed: its peers reach its
// services through its awl address anyway.
type ExitNodeRule struct {
	awlPrefixes []netip.Prefix
}

func (r *ExitNodeRule) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	if req.DestAddr == nil {
		return ctx, false
	}
	addr, ok := netip.AddrFromSlice(req.DestAddr.IP)
	if !ok {
		return ctx, false
	}

	return ctx, isAllowedExitAddr(addr, r.awlPrefixes)
}

func isAllowedExitAddr(addr netip.Addr, awlPrefixes []netip.Prefix) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsMulticast() ||
		privatenet.Contains(addr) {
		return false
	}
	for _, prefix := range awlPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}

	return true
}

// exitNodeResolver refuses .awl names before they reach the system resolver:
// they are local to each node, and a resolver that does not ask awl DNS would
// send them out.
type exitNodeResolver struct {
	socks5.DNSResolver
}

func (r exitNodeResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	if isAwlName(name) {
		return ctx, nil, fmt.Errorf("%s names are not proxied", awldns.LocalDomain)
	}
	return r.DNSResolver.Resolve(ctx, name)
}

func isAwlName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	return name == awldns.LocalDomain || strings.HasSuffix(name, "."+awldns.LocalDomain)
}

type Logger struct {
	logger *log.ZapEventLogger
}

func NewLogger() *Logger {
	return &Logger{
		logger: log.Logger("socks5/server"),
	}
}

func (l *Logger) Printf(format string, v ...interface{}) {
	// TODO: make more configurable log levels in upstream
	l.logger.Infof(format, v...)
}
