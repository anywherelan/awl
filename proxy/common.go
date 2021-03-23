package proxy

import (
	"io"
	"net"

	"github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p-core/network"
)

const bufSize = 32 * 1024

var logger = log.Logger("awl/proxy")

type temporary interface {
	Temporary() bool
}

func handleStream(conn net.Conn, stream network.Stream) {
	// Copy from stream to conn
	go copyStream(stream, conn)

	// Copy from conn to stream
	go copyStream(conn, stream)
}

func copyStream(from io.ReadCloser, to io.WriteCloser) {
	buf := pool.Get(bufSize)

	defer func() {
		pool.Put(buf)
		_ = from.Close()
		_ = to.Close()
	}()
	_, err := io.CopyBuffer(to, from, buf)
	if err != nil {
		logger.Debugf("Error while copying stream: %v", err)
	}
}
