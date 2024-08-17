package socks5

import (
	"errors"
	"net"
	"time"
)

type Client struct {
	listener net.Listener
	connsCh  chan net.Conn
}

func NewClient(listenAddr string) (*Client, error) {
	// TODO: add support for udp?
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	cli := Client{
		listener: listener,
		connsCh:  make(chan net.Conn, 1),
	}
	go func() {
		_ = cli.serve()
		// TODO: log err
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
			// TODO: log
		}
	}
}
