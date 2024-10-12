package socks5

import (
	"errors"
	"net"
	"time"

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
