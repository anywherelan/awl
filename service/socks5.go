package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/metrics"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/socks5"
)

// SocketMarker is the slice of *netstate.Manager the SOCKS5 service needs:
// the control function that marks exit-node dial sockets to bypass our own
// VPN gateway routes.
type SocketMarker interface {
	ControlFunc() func(network, address string, c syscall.RawConn) error
}

type SOCKS5 struct {
	logger *log.ZapEventLogger
	p2p    P2p
	conf   *config.Config

	client *socks5.Client
	server *socks5.Server
}

// NewSOCKS5 starts the local SOCKS5 proxy if it is enabled. If it fails to
// start, e.g. its port is taken, the proxy stays off and the rest of awl works
// without it.
func NewSOCKS5(p2pService P2p, conf *config.Config, sockMarker SocketMarker) *SOCKS5 {
	logger := log.Logger("awl/service/socks5")

	awlPrefixes := []netip.Prefix{conf.VPNPrefix().Masked()}
	if prefixV6, ok := conf.VPNPrefixV6(); ok {
		awlPrefixes = append(awlPrefixes, prefixV6.Masked())
	}

	var client *socks5.Client
	if conf.SOCKS5.ListenerEnabled {
		var err error
		client, err = socks5.NewClient(conf.SOCKS5.ListenAddress, conf.SOCKS5.Username, conf.SOCKS5.Password, awlPrefixes)
		if err != nil {
			logger.Errorf("socks5 proxy failed to start: %v. Free the port or change socks5.listenAddress in the config", err)
		} else {
			logger.Infof("started socks5 proxy on socks5://%s", client.Addr())
		}
	}

	// Mark the exit-node's outbound sockets so they bypass our own VPN gateway
	// default route (if any), mirroring libp2p socket marking. sockMarker is
	// always set by Application.Init; ControlFunc may still be nil on platforms
	// without socket marking, which NewServer handles.
	var dialControl func(network, address string, c syscall.RawConn) error
	if sockMarker != nil {
		dialControl = sockMarker.ControlFunc()
	}
	server := socks5.NewServer(dialControl, awlPrefixes)
	socks := &SOCKS5{
		logger: logger,
		p2p:    p2pService,
		conf:   conf,
		client: client,
		server: server,
	}

	return socks
}

func (s *SOCKS5) Close() {
	if s.client != nil {
		_ = s.client.Close()
	}
}

// ListenAddress returns the address the local SOCKS5 proxy listens on, or an
// empty string if the proxy is off.
func (s *SOCKS5) ListenAddress() string {
	if s.client == nil {
		return ""
	}
	return s.client.Addr().String()
}

func (s *SOCKS5) ListAvailableProxies() []entity.AvailableProxy {
	s.conf.RLock()
	proxies := []entity.AvailableProxy{}
	for _, peer := range s.conf.KnownPeers {
		if !peer.AllowedUsingAsExitNode {
			continue
		}

		proxy := entity.AvailableProxy{
			PeerID:    peer.PeerID,
			PeerName:  peer.DisplayName(),
			Connected: s.p2p.IsConnected(peer.PeerId()),
		}
		proxies = append(proxies, proxy)
	}
	s.conf.RUnlock()

	slices.SortFunc(proxies, func(a, b entity.AvailableProxy) int {
		if a.Connected != b.Connected {
			if a.Connected {
				return -1
			}
			return 1
		}
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

func (s *SOCKS5) GetProxyPeerID() string {
	s.conf.RLock()
	defer s.conf.RUnlock()
	return s.conf.SOCKS5.UsingPeerID
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
		if stream.Protocol() != protocol.Socks5NoAuthMethod {
			_ = s.server.SendServerFailureReply(stream)
		}
		return
	}

	// ignore error, we can do nothing about it
	if stream.Protocol() == protocol.Socks5NoAuthMethod {
		_ = s.server.ServeStreamConnNoAuth(stream)
	} else {
		_ = s.server.ServeStreamConn(stream)
	}

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
				s.logger.Debugf("SOCKS5 proxy client connection error: %v", err)
			}
		}()
	}
}

// SetExitNodeRulesEnabled is for tests: with the rules off, the exit node
// proxies to local addresses too, see socks5.ExitNodeRule.
func (s *SOCKS5) SetExitNodeRulesEnabled(enabled bool) {
	s.server.SetRulesEnabled(enabled)
}

// SetBypassLocalAwl is for tests: with the bypass off, the proxy client sends
// every target to the exit node, localhost included, see socks5.Client.
func (s *SOCKS5) SetBypassLocalAwl(enabled bool) {
	if s.client != nil {
		s.client.SetBypassLocalAwl(enabled)
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

	return s.client.ServeConn(ctx, conn, s.proxyToExitNode)
}

// proxyToExitNode is the socks5.ForwardFunc of the client: it passes the
// request, already read from conn as rawReq, to the exit node and then pipes
// the connection through it. An error means nothing has been sent to the
// client yet.
func (s *SOCKS5) proxyToExitNode(ctx context.Context, conn net.Conn, rawReq []byte) error {
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

	stream, err := s.p2p.NewStreamMulti(ctx, remotePeerID, protocol.Socks5NoAuthMethod, protocol.Socks5PacketMethod)
	if err != nil {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "peer_stream_failed").Inc()
		return err
	}
	defer func() {
		_ = stream.Reset()
	}()

	// An exit node older than v0.17.0 runs the auth negotiation itself.
	if stream.Protocol() == protocol.Socks5PacketMethod {
		if err := socks5.NoAuthHandshake(stream); err != nil {
			metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "peer_stream_failed").Inc()
			return err
		}
	}
	if _, err := stream.Write(rawReq); err != nil {
		metrics.SOCKS5ErrorsTotal.WithLabelValues("client", "peer_stream_failed").Inc()
		return err
	}

	socks5.Pipe(conn, stream)

	// stream.Write() + stream.Reset() are not guaranteed to run sequentially
	// e.g reader on the other side may not read everything we sent because of stream.Reset()
	// in case of socks5 errors (small payload), receiver could get EOF
	// TODO: make better workaround for this. stream.CloseWrite(), etc doesn't help
	time.Sleep(50 * time.Millisecond)

	return nil
}
