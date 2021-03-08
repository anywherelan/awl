package proxy

import (
	"net"
	"strings"

	"github.com/libp2p/go-libp2p-core/network"
)

type ServerProxy interface {
	SetupServer(address string) error
	AcceptConnections(streamFactory func() (network.Stream, error))
	Protocol() string
	ListenAddress() string
	Close()
}

type TCPServerProxy struct {
	listener net.Listener
	proto    string
}

func (p *TCPServerProxy) SetupServer(address string) error {
	listener, err := net.Listen(p.proto, address)
	if err != nil {
		return err
	}
	p.listener = listener
	return nil
}

func (p *TCPServerProxy) AcceptConnections(streamFactory func() (network.Stream, error)) {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if tmp, ok := err.(temporary); ok && tmp.Temporary() {
				continue
			} else if netErr, ok := err.(*net.OpError); ok &&
				strings.HasSuffix(netErr.Err.Error(), "use of closed network connection") {
				return
			}
			logger.Errorf("Error while accepting connection: %v", err)
			return
		}
		stream, err := streamFactory()
		if err != nil {
			logger.Warnf("Create stream failed: %v", err)
			_ = conn.Close()
			continue
		}
		handleStream(conn, stream)
	}
}

func (p *TCPServerProxy) Protocol() string {
	return p.proto
}

func (p *TCPServerProxy) ListenAddress() string {
	return p.listener.Addr().String()
}

func (p *TCPServerProxy) Close() {
	if p.listener != nil {
		err := p.listener.Close()
		if err != nil {
			logger.Errorf("TCPServerProxy close listener err: %v", err)
		}
	}
}

func NewTCPServerProxy() ServerProxy {
	return &TCPServerProxy{proto: "tcp"}
}
