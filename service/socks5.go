package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

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
	logger := log.Logger("awl/service/socks5")

	var client *socks5.Client
	if conf.SOCKS5.ListenerEnabled {
		var err error
		client, err = socks5.NewClient(conf.SOCKS5.ListenAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to start socks5 listener: %v", err)
		}

		logger.Infof("started socks5 proxy on socks5://%s", conf.SOCKS5.ListenAddress)
	}

	server := socks5.NewServer()
	socks := &SOCKS5{
		logger: logger,
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
		_ = stream.Reset()
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

	// ignore error, we can do nothing about it
	_ = s.server.ServeStreamConn(stream)

	// stream.Write() + stream.Reset() are not guaranteed to run sequentially
	// e.g reader on the other side may not read everything we sent because of stream.Reset()
	// in case of socks5 errors (small payload), receiver could get EOF
	// TODO: make better workaround for this. stream.CloseWrite(), etc doesn't help
	time.Sleep(20 * time.Millisecond)
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

// SetProxyingLocalhostEnabled is created for tests and not intended for real usage.
func (s *SOCKS5) SetProxyingLocalhostEnabled(enabled bool) {
	if enabled {
		s.server.SetRules(socks5.NewRulePermitAll())
	} else {
		s.server.SetRules(socks5.NewRuleDenyLocalhost())
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
		return errors.New("no peer is set for proxy")
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
		_ = stream.Reset()
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
		s.copyStream(stream, conn)

		// in some cases stream could finish writing before conn (e.g for errors)
		// without closing conn we will have a deadlock
		_ = conn.Close()
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
