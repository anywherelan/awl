package socks5

import (
	"errors"
	"fmt"
	"net"
	"time"

	socks5Lib "github.com/haxii/socks5"
	"github.com/ipfs/go-log/v2"
)

type Client struct {
	listener net.Listener
	connsCh  chan net.Conn
	logger   *log.ZapEventLogger
}

func NewClient(listenAddr string) (*Client, error) {
	// TODO: add support for udp?
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	logger := log.Logger("socks5/client")

	cli := Client{
		listener: listener,
		connsCh:  make(chan net.Conn, 1),
		logger:   logger,
	}
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

func (c *Client) ConnsChan() <-chan net.Conn {
	return c.connsCh
}

// HandleLocalAuth performs the SOCKS5 auth negotiation locally, responding with NoAuth.
// This avoids sending the auth handshake over the network to the remote peer.
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

	// Check NoAuth is offered
	hasNoAuth := false
	for _, m := range methods {
		if m == socks5Lib.AuthMethodNoAuth {
			hasNoAuth = true
			break
		}
	}
	if !hasNoAuth {
		_, _ = conn.Write([]byte{0x05, socks5Lib.AuthMethodNoAcceptable})
		return fmt.Errorf("client does not support NoAuth method")
	}

	// Respond: NoAuth selected
	_, err = conn.Write([]byte{0x05, socks5Lib.AuthMethodNoAuth})
	return err
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
