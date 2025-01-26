package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
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

func (s *SOCKS5) ListAvailableProxies() []entity.AvailableProxy {
	s.conf.RLock()
	proxies := []entity.AvailableProxy{}
	for _, peer := range s.conf.KnownPeers {
		if !peer.AllowedUsingAsExitNode {
			continue
		}

		if !s.p2p.IsConnected(peer.PeerId()) {
			continue
		}

		proxy := entity.AvailableProxy{
			PeerID:   peer.PeerID,
			PeerName: peer.DisplayName(),
		}
		proxies = append(proxies, proxy)
	}
	s.conf.RUnlock()

	slices.SortFunc(proxies, func(a, b entity.AvailableProxy) int {
		return strings.Compare(a.PeerName, b.PeerName)
	})

	return proxies
}

func (s *SOCKS5) SetProxyPeerID(peerID string) {
	s.conf.Lock()
	s.conf.SOCKS5.UsingPeerID = peerID
	s.conf.Unlock()
	s.conf.Save()
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
			defer func() {
				_ = conn.Close()
			}()

			err := s.proxyConn(ctx, conn)
			if err != nil {
				_ = s.server.SendServerFailureReply(conn)
			}
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
	s.conf.RLock()
	usePeerID := s.conf.SOCKS5.UsingPeerID
	s.conf.RUnlock()

	if usePeerID == "" {
		return errors.New("no peer is set for proxy")
	}

	peer, exists := s.conf.GetPeer(usePeerID)
	if !exists || !peer.AllowedUsingAsExitNode {
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
	// TODO: SetDeadline on conn for ~5 min just in case?
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Copy from conn to stream
		_ = s.copyStream(conn, stream)
	}()

	go func() {
		defer wg.Done()
		// Copy from stream to conn
		_ = s.copyStream(stream, conn)
	}()

	wg.Wait()
}

func (s *SOCKS5) copyStream(from io.ReadCloser, to io.WriteCloser) error {
	const bufSize = 32 * 1024
	buf := pool.Get(bufSize)

	defer func() {
		pool.Put(buf)
	}()
	_, err := io.CopyBuffer(to, from, buf)

	type closeWriter interface {
		CloseWrite() error
	}
	if conn, ok := to.(closeWriter); ok {
		_ = conn.CloseWrite()
	}

	type closeReader interface {
		CloseRead() error
	}
	if conn, ok := from.(closeReader); ok {
		_ = conn.CloseRead()
	}

	return err
}
