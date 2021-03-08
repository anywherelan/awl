package proxy

import (
	"fmt"
	"net"

	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/network"
)

type ClientProxy interface {
	Proxy(stream network.Stream, address string) error
	Close()
}

type TCPClientProxy struct {
	conn   net.Conn
	stream network.Stream
}

func (p *TCPClientProxy) Proxy(stream network.Stream, address string) error {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return fmt.Errorf("unable to connect %s: %v", address, err)
	}
	p.conn = conn
	p.stream = stream

	handleStream(conn, stream)
	return nil
}

func (p *TCPClientProxy) Close() {
	if p.conn != nil {
		err := p.conn.Close()
		if err != nil {
			logger.Warnf("TCPClientProxy close conn: %v", err)
		}
	}
	if p.stream != nil {
		err := helpers.FullClose(p.stream)
		if err != nil {
			logger.Warnf("TCPClientProxy close incoming stream: %v", err)
		}
	}
}

func NewTCPClientProxy() ClientProxy {
	return &TCPClientProxy{}
}
