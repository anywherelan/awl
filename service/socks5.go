package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	socks5Proxy "github.com/haxii/socks5"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/metrics"
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
	metrics.SOCKS5ConnectionsTotal.WithLabelValues("server").Inc()
	metrics.SOCKS5ActiveConnections.WithLabelValues("server").Inc()
	start := time.Now()
	defer func() {
		metrics.SOCKS5ActiveConnections.WithLabelValues("server").Dec()
		metrics.SOCKS5ConnectionDurationSeconds.WithLabelValues("server").Observe(time.Since(start).Seconds())
		_ = stream.Reset()
	}()

	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	knownPeer, known := s.conf.GetPeer(peerID)
	if !known {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("server", "denied").Inc()
		s.logger.Infof("Unknown peer %s tried to socks5 proxy", peerID)
		return
	}
	if !knownPeer.WeAllowUsingAsExitNode {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("server", "denied").Inc()
		s.logger.Infof("Peer %s without rights tried to socks5 proxy", peerID)
		return
	}

	s.conf.RLock()
	enabled := s.conf.SOCKS5.ProxyingEnabled
	s.conf.RUnlock()

	if !enabled {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("server", "proxying_disabled").Inc()
		_ = s.server.SendServerFailureReply(stream)
		return
	}

	// ignore error, we can do nothing about it
	_ = s.server.ServeStreamConn(stream)

	// stream.Write() + stream.Reset() are not guaranteed to run sequentially
	// e.g reader on the other side may not read everything we sent because of stream.Reset()
	// in case of socks5 errors (small payload), receiver could get EOF
	// TODO: make better workaround for this. stream.CloseWrite(), etc doesn't help
	time.Sleep(50 * time.Millisecond)
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

			s.logger.Debug("got new SOCKS5 proxy client connection")

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
	metrics.SOCKS5ConnectionsTotal.WithLabelValues("client").Inc()
	metrics.SOCKS5ActiveConnections.WithLabelValues("client").Inc()
	start := time.Now()
	defer func() {
		metrics.SOCKS5ActiveConnections.WithLabelValues("client").Dec()
		metrics.SOCKS5ConnectionDurationSeconds.WithLabelValues("client").Observe(time.Since(start).Seconds())
	}()

	s.conf.RLock()
	usePeerID := s.conf.SOCKS5.UsingPeerID
	s.conf.RUnlock()

	if usePeerID == "" {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "no_proxy_peer").Inc()
		return errors.New("no peer is set for proxy")
	}

	peer, exists := s.conf.GetPeer(usePeerID)
	if !exists || !peer.AllowedUsingAsExitNode {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "peer_not_allowed").Inc()
		return fmt.Errorf("configured proxy peer %s does not allow us to proxy traffic", usePeerID)
	}

	remotePeerID := peer.PeerId()
	err := s.p2p.ConnectPeer(ctx, remotePeerID)
	if err != nil {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "peer_connect_failed").Inc()
		return err
	}

	stream, err := s.p2p.NewStream(ctx, remotePeerID, protocol.Socks5PacketMethod)
	if err != nil {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "peer_stream_failed").Inc()
		return err
	}
	defer func() {
		_ = stream.Reset()
	}()

	s.handleStream(conn, stream)

	// stream.Write() + stream.Reset() are not guaranteed to run sequentially
	// e.g reader on the other side may not read everything we sent because of stream.Reset()
	// in case of socks5 errors (small payload), receiver could get EOF
	// TODO: make better workaround for this. stream.CloseWrite(), etc doesn't help
	time.Sleep(50 * time.Millisecond)

	return nil
}

func (s *SOCKS5) handleStream(conn net.Conn, stream network.Stream) {
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		// Copy from conn to stream
		_ = socks5Proxy.ProxyStream(conn, stream)
	}()

	// Copy from stream to conn
	_ = socks5Proxy.ProxyStream(stream, conn)

	<-doneCh
}
