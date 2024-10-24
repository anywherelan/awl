package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"

	"github.com/haxii/socks5"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
)

const (
	socks5Version = uint8(5)
)

type Server struct {
	socks *socks5.Server
	conf  *socks5.Config
	rule  *UpdatableRule
}

func NewServer() *Server {
	rule := NewUpdatableRule(NewRuleDenyLocalhost())
	conf := &socks5.Config{
		// fake addr, we don't bind address for server
		BindIP:   net.IPv4(127, 0, 0, 1),
		BindPort: 8000,
		Rules:    rule,
		Logger:   NewLogger(),
		Resolver: nil,
		// TODO: add optional password authentication method support
	}
	server, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}

	return &Server{
		socks: server,
		conf:  conf,
		rule:  rule,
	}
}

// SetRules is created for tests and not intended for real usage.
func (s *Server) SetRules(rule socks5.RuleSet) {
	s.rule.SetRule(rule)
}

func (s *Server) ServeStreamConn(stream network.Stream) error {
	conn := StreamConnWrapper{Stream: stream}
	return s.socks.ServeConn(conn)
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

type RuleDenyLocalhost struct {
	ipNet *net.IPNet
}

func NewRulePermitAll() socks5.RuleSet {
	return socks5.PermitAll()
}

func NewRuleDenyLocalhost() *RuleDenyLocalhost {
	_, ipNet, err := net.ParseCIDR("127.0.0.1/8")
	if err != nil {
		// verified in tests
		panic(err)
	}

	return &RuleDenyLocalhost{
		ipNet: ipNet,
	}
}

func (r *RuleDenyLocalhost) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	addr := req.DestAddr
	if addr == nil {
		return ctx, true
	}

	if r.ipNet.Contains(addr.IP) {
		return ctx, false
	}

	return ctx, true
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
