package socks5

import (
	"io"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

var _ net.Conn = (*ReadWriterConnWrapper)(nil)

// ReadWriterConnWrapper is used for socks5
type ReadWriterConnWrapper struct {
	io.ReadWriteCloser
}

func (c ReadWriterConnWrapper) LocalAddr() net.Addr {
	return nil
}

func (c ReadWriterConnWrapper) RemoteAddr() net.Addr {
	// TODO: implement
	//  we probably need this to return net.TCPAddr, to have correct address in proxy
	return nil
}

func (c ReadWriterConnWrapper) SetDeadline(time.Time) error {
	return nil
}

func (c ReadWriterConnWrapper) SetReadDeadline(time.Time) error {
	return nil
}

func (c ReadWriterConnWrapper) SetWriteDeadline(time.Time) error {
	return nil
}

var _ net.Conn = (*StreamConnWrapper)(nil)

// StreamConnWrapper is used for socks5
type StreamConnWrapper struct {
	network.Stream
}

func (c StreamConnWrapper) LocalAddr() net.Addr {
	return nil
}

func (c StreamConnWrapper) RemoteAddr() net.Addr {
	// TODO: implement
	//  we probably need this to return net.TCPAddr, to have correct address in proxy
	return nil
}

func (c StreamConnWrapper) Close() error {
	// Close in wrapper is no-op to not close underlying conn inside socks5 library
	// Stream is closed at service layer
	return nil
}
