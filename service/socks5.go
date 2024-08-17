package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/socks5"
)

type SOCKS5 struct {
	logger *log.ZapEventLogger
	p2p    P2p
	conf   *config.Config

	client *socks5.Client
	server *socks5.Server
}

func NewSOCKS5(p2pService P2p, conf *config.Config) (*SOCKS5, error) {
	var client *socks5.Client
	if conf.SOCKS5.ListenerEnabled {
		var err error
		client, err = socks5.NewClient(conf.SOCKS5.ListenAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to start socks5 listener: %v", err)
		}
	}

	server := socks5.NewServer()
	socks := &SOCKS5{
		logger: log.Logger("awl/service/socks5"),
		p2p:    p2pService,
		conf:   conf,
		client: client,
		server: server,
	}

	return socks, nil
}

func (s *SOCKS5) Close() {
	if s.client != nil {
		_ = s.client.Close()
	}
}

func (s *SOCKS5) ProxyStreamHandler(stream network.Stream) {
	defer func() {
		_ = stream.Close()
	}()

	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	knownPeer, known := s.conf.GetPeer(peerID)
	if !known {
		s.logger.Infof("Unknown peer %s tried to socks5 proxy", peerID)
		return
	}
	if !knownPeer.WeAllowUsingAsExitNode {
		s.logger.Infof("Peer %s without rights tried to socks5 proxy", peerID)
		return
	}

	s.conf.RLock()
	enabled := s.conf.SOCKS5.ProxyingEnabled
	s.conf.RUnlock()

	if !enabled {
		_ = s.server.SendServerFailureReply(stream)
		return
	}

	_ = s.server.ServeStreamConn(stream)
	// ignore error, we can do nothing about it
}

func (s *SOCKS5) ServeConns(ctx context.Context) {
	if s.client == nil {
		return
	}

	proxyConns := s.client.ConnsChan()
	for conn := range proxyConns {
		go func() {
			// TODO: check err?
			_ = s.proxyConn(ctx, conn)
		}()
	}
}

func (s *SOCKS5) proxyConn(ctx context.Context, conn net.Conn) error {
	defer func() {
		_ = conn.Close()
	}()

	s.conf.RLock()
	usePeerID := s.conf.SOCKS5.UsingPeerID
	s.conf.RUnlock()

	if usePeerID == "" {
		_ = s.server.SendServerFailureReply(conn)
		return errors.New("no usable peer")
	}

	peer, exists := s.conf.GetPeer(usePeerID)
	if !exists || !peer.AllowedUsingAsExitNode {
		_ = s.server.SendServerFailureReply(conn)
		return fmt.Errorf("configured proxy peer %s don't allow us proxying", usePeerID)
	}

	remotePeerID := peer.PeerId()
	err := s.p2p.ConnectPeer(ctx, remotePeerID)
	if err != nil {
		return err
	}

	stream, err := s.p2p.NewStream(ctx, remotePeerID, protocol.Socks5PacketMethod)
	if err != nil {
		return err
	}
	defer func() {
		_ = stream.Close()
	}()

	s.handleStream(conn, stream)

	return nil
}

func (s *SOCKS5) handleStream(conn net.Conn, stream network.Stream) {
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Copy from conn to stream
		s.copyStream(conn, stream)
	}()

	go func() {
		defer wg.Done()
		// Copy from stream to conn
		go s.copyStream(stream, conn)
	}()

	wg.Wait()
}

func (s *SOCKS5) copyStream(from io.ReadCloser, to io.WriteCloser) {
	const bufSize = 32 * 1024
	buf := pool.Get(bufSize)

	defer func() {
		pool.Put(buf)
	}()
	_, _ = io.CopyBuffer(to, from, buf)
	// ignore error, we can do nothing about it
}
