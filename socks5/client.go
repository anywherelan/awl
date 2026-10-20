package socks5

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	socks5Lib "github.com/haxii/socks5"
	"github.com/ipfs/go-log/v2"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/metrics"
)

type Client struct {
	listener      net.Listener
	connsCh       chan net.Conn
	logger        *log.ZapEventLogger
	authenticator socks5Lib.Authenticator
	awlPrefixes   []netip.Prefix
	// bypassLocalAwl lets clients on this host reach localhost and the awl
	// network without the exit node, see bypassTarget.
	bypassLocalAwl atomic.Bool
}

// ForwardFunc passes a request the client does not handle itself to the exit
// node. rawReq is the request as the SOCKS client sent it, already read from
// conn. An error means nothing has been sent to the SOCKS client yet.
type ForwardFunc func(ctx context.Context, conn net.Conn, rawReq []byte) error

// NewClient starts the SOCKS5 listener. awlPrefixes are the awl subnets, IPv4
// and IPv6, which the client connects to itself (see bypassTarget).
func NewClient(listenAddr string, username, password string, awlPrefixes []netip.Prefix) (*Client, error) {
	// TODO: add support for udp?
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	logger := log.Logger("socks5/client")

	var authenticator socks5Lib.Authenticator
	if username != "" && password != "" {
		authenticator = socks5Lib.UserPassAuthenticator{
			Credentials: socks5Lib.StaticCredentials{username: password},
		}
	} else {
		authenticator = socks5Lib.NoAuthAuthenticator{}
	}

	cli := Client{
		listener:      listener,
		connsCh:       make(chan net.Conn, 1),
		logger:        logger,
		authenticator: authenticator,
		awlPrefixes:   awlPrefixes,
	}
	cli.bypassLocalAwl.Store(true)
	go func() {
		serveErr := cli.serve()
		if serveErr != nil {
			logger.Errorf("serving listener error, stopped serving: %v", serveErr)
		}
	}()

	return &cli, nil
}

func (c *Client) Close() error {
	return c.listener.Close()
}

// Addr returns the address the listener is bound to.
func (c *Client) Addr() net.Addr {
	return c.listener.Addr()
}

func (c *Client) ConnsChan() <-chan net.Conn {
	return c.connsCh
}

// SetBypassLocalAwl is for tests: with the bypass off, every target goes to the
// exit node, localhost included.
func (c *Client) SetBypassLocalAwl(enabled bool) {
	c.bypassLocalAwl.Store(enabled)
}

// ServeConn serves a connection from ConnsChan: it runs the auth negotiation
// and reads the request. It connects to a target on this host or in the awl
// network itself, see bypassTarget, and passes other requests to forward.
func (c *Client) ServeConn(ctx context.Context, conn net.Conn, forward ForwardFunc) error {
	if err := c.HandleLocalAuth(conn); err != nil {
		return err
	}
	req, rawReq, err := readRequest(conn)
	if err != nil {
		return err
	}

	// Only a client on this host bypasses the exit node: a device on the LAN,
	// if the listener is open to it, must not reach our localhost and our peers.
	if c.bypassLocalAwl.Load() && isLoopbackConn(conn) {
		if target, ok := bypassTarget(req.DestAddr, c.awlPrefixes); ok {
			return connectDirect(ctx, conn, req.Command, target)
		}
	}

	err = forward(ctx, conn, rawReq)
	if err != nil {
		_ = sendReply(conn, socks5Lib.ReplyServerFailure, nil)
	}

	return err
}

// HandleLocalAuth performs the SOCKS5 auth negotiation locally.
// It reads the version byte and offered methods, then delegates to the configured authenticator.
func (c *Client) HandleLocalAuth(conn net.Conn) error {
	// Read version byte
	version := []byte{0}
	if _, err := conn.Read(version); err != nil {
		return fmt.Errorf("failed to read version: %w", err)
	}
	if version[0] != 0x05 {
		return fmt.Errorf("unsupported SOCKS version: %d", version[0])
	}

	// Read offered auth methods
	methods, err := socks5Lib.ReadMethods(conn)
	if err != nil {
		return fmt.Errorf("failed to read auth methods: %w", err)
	}

	// Check if the client offers our required method
	requiredMethod := c.authenticator.GetCode()
	hasMethod := false
	for _, m := range methods {
		if m == requiredMethod {
			hasMethod = true
			break
		}
	}
	if !hasMethod {
		_, _ = conn.Write([]byte{0x05, socks5Lib.AuthMethodNoAcceptable})
		return fmt.Errorf("client does not support required auth method %d", requiredMethod)
	}

	// Delegate to the authenticator (writes method selection + handles subnegotiation)
	_, err = c.authenticator.Authenticate(conn, conn)
	return err
}

// readRequest reads a request that follows the auth negotiation. It returns the
// bytes it read too, to pass the request on to the exit node as it is: it reads
// no further than the request, so the data after it stays in r.
func readRequest(r io.Reader) (*socks5Lib.Request, []byte, error) {
	var raw bytes.Buffer
	req, err := socks5Lib.NewRequest(io.TeeReader(r, &raw))
	if err != nil {
		return nil, nil, err
	}

	return req, raw.Bytes(), nil
}

// bypassTarget returns the address to connect to for a target on this host or
// in the awl network: a name in .awl, localhost, or a loopback address or an
// address in the awl subnets. ok is false for a target that goes to the exit
// node. A name in .awl is left to the system resolver, which is awl DNS.
//
// Only the target as the client gives it counts. Other names are not resolved
// here, so a name pointing to 127.0.0.1 goes to the exit node, which refuses
// it: otherwise any site could get to our localhost through DNS rebinding.
func bypassTarget(dest *socks5Lib.AddrSpec, awlPrefixes []netip.Prefix) (target string, ok bool) {
	port := strconv.Itoa(dest.Port)
	if dest.FQDN != "" {
		switch {
		case awldns.InLocalDomain(dest.FQDN):
			return net.JoinHostPort(dest.FQDN, port), true
		case isLocalhostName(dest.FQDN):
			return net.JoinHostPort("localhost", port), true
		}
	}

	addr, valid := destIP(dest)
	if !valid {
		return "", false
	}
	addr = addr.Unmap()
	inAwlSubnet := slices.ContainsFunc(awlPrefixes, func(prefix netip.Prefix) bool {
		return prefix.Contains(addr)
	})
	if addr.IsLoopback() || inAwlSubnet {
		return net.JoinHostPort(addr.String(), port), true
	}

	return "", false
}

// destIP returns the IP address of dest. A name is parsed as an IP address:
// Chrome sends IP addresses as names too.
func destIP(dest *socks5Lib.AddrSpec) (netip.Addr, bool) {
	if dest.FQDN == "" {
		return netip.AddrFromSlice(dest.IP)
	}
	addr, err := netip.ParseAddr(dest.FQDN)
	return addr, err == nil
}

// connectDirect connects to a target from bypassTarget itself. The socket is
// not marked: traffic to peers has to go through the TUN.
func connectDirect(ctx context.Context, conn net.Conn, command uint8, target string) error {
	if command != socks5Lib.CommandConnect {
		_ = sendReply(conn, socks5Lib.ReplyCommandNotSupported, nil)
		return fmt.Errorf("unsupported command %d to %s", command, target)
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	targetConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "direct_dial_failed").Inc()
		_ = sendReply(conn, dialErrorReply(err), nil)
		return err
	}
	defer func() {
		_ = targetConn.Close()
	}()

	err = sendReply(conn, socks5Lib.ReplySucceeded, nil)
	if err != nil {
		return err
	}
	Pipe(conn, targetConn)

	return nil
}

// Pipe copies data both ways between the SOCKS client and the target until
// both directions are done.
func Pipe(conn net.Conn, target io.ReadWriter) {
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		_ = socks5Lib.ProxyStream(conn, target)
	}()

	_ = socks5Lib.ProxyStream(target, conn)

	<-doneCh
}

// NoAuthHandshake runs the auth negotiation with an exit node that expects it
// (protocol.Socks5PacketMethod): it offers no auth, as the client has been
// authenticated locally.
func NoAuthHandshake(rw io.ReadWriter) error {
	if _, err := rw.Write([]byte{socks5Version, 1, socks5Lib.AuthMethodNoAuth}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(rw, reply); err != nil {
		return fmt.Errorf("failed to read auth method: %w", err)
	}
	if reply[0] != socks5Version || reply[1] != socks5Lib.AuthMethodNoAuth {
		return fmt.Errorf("unexpected auth method reply %v", reply)
	}

	return nil
}

// dialErrorReply returns the reply to a request that failed to connect, the
// same one the exit node gives.
func dialErrorReply(err error) uint8 {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "refused"):
		return socks5Lib.ReplyConnectionRefused
	case strings.Contains(msg, "network is unreachable"):
		return socks5Lib.ReplyNetworkUnreachable
	default:
		return socks5Lib.ReplyHostUnreachable
	}
}

func isLoopbackConn(conn net.Conn) bool {
	addr, ok := conn.RemoteAddr().(*net.TCPAddr)
	return ok && addr.IP.IsLoopback()
}

// isLocalhostName reports whether name is localhost or a name under it, which
// is always the local host (RFC 6761).
func isLocalhostName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	return name == "localhost" || strings.HasSuffix(name, ".localhost")
}

func (c *Client) serve() error {
	defer close(c.connsCh)

	for {
		conn, err := c.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		timer := time.NewTimer(time.Second)
		select {
		case c.connsCh <- conn:
			// ok
			timer.Stop()
		case <-timer.C:
			c.logger.Error("couldn't process conn, closed and dropped conn")
			_ = conn.Close()
		}
	}
}
