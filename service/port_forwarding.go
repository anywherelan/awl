package service

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/peerlan/peerlan/config"
	"github.com/peerlan/peerlan/entity"
	"github.com/peerlan/peerlan/protocol"
	"github.com/peerlan/peerlan/proxy"
)

const (
	portStreamKey = "port"
)

type (
	PortForwarding struct {
		forwarded forwardedPortsRegistry
		logger    *log.ZapEventLogger
		p2p       *P2pService
		conf      *config.Config
	}
	forwardedPortsRegistry struct {
		sync.RWMutex
		// LocalPort -> struct
		ports map[forwardedPort]forwardedPortInfo
	}
	forwardedPort struct {
		RemotePort int
		PeerId     peer.ID
	}
	forwardedPortInfo struct {
		LocalPort  int
		RemotePort int
		PeerId     peer.ID
		tcpProxy   proxy.ServerProxy
	}
)

func NewPortForwarding(p2pService *P2pService, conf *config.Config) *PortForwarding {
	p := &PortForwarding{
		forwarded: forwardedPortsRegistry{
			ports: make(map[forwardedPort]forwardedPortInfo),
		},
		logger: log.Logger("peerlan/service/forwarding"),
		p2p:    p2pService,
		conf:   conf,
	}
	p2pService.RegisterOnPeerConnected(p.onPeerConnected)
	p2pService.RegisterOnPeerDisconnected(p.onPeerDisconnected)

	return p
}

func (s *PortForwarding) StreamHandler(stream network.Stream) {
	peerID := stream.Conn().RemotePeer().String()
	port, err := protocol.HandleForwardPortStream(stream)
	if err != nil {
		s.logger.Errorf("unable to read port forwarding stream: %v", err)
		return
	}
	s.logger.Debugf("got stream to forward %d port for peer %s", port, peerID)

	if !s.conf.CheckLocalPerm(peerID, port) {
		s.logger.Warnf("peer %s tried to access forbidden port %d", peerID, port)
		go helpers.FullClose(stream)
		return
	}
	// TODO: remove hardcode?
	address := "127.0.0.1:" + strconv.Itoa(port)
	tcpProxy := proxy.NewTCPClientProxy()
	err = tcpProxy.Proxy(stream, address)
	if err != nil {
		tcpProxy.Close()
		s.logger.Errorf("forwarding stream to local port: %v", err)
	} else {
		setStreamPort(stream, port)
	}
}

func (s *PortForwarding) ForwardPort(peerID peer.ID, ipAddr string, localPort, remotePort int) error {
	err := s.p2p.ConnectPeer(context.Background(), peerID)
	if err != nil {
		return err
	}

	if handled, err := s.checkForwardedPort(peerID, localPort, remotePort); handled {
		return err
	}

	serverAddr := fmt.Sprintf("%s:%d", ipAddr, localPort)
	tcpServerProxy := proxy.NewTCPServerProxy()
	err = tcpServerProxy.SetupServer(serverAddr)
	if err != nil {
		return fmt.Errorf("unable to bind address %s: %v", serverAddr, err)
	}

	go tcpServerProxy.AcceptConnections(func() (network.Stream, error) {
		stream, err := s.p2p.NewStream(peerID, protocol.PortForwardingMethod)
		if err != nil {
			return nil, err
		}
		setStreamPort(stream, remotePort)
		portPacket := protocol.PackForwardPortData(remotePort)
		_, err = stream.Write(portPacket)
		return stream, err
	})

	s.addForwardedPort(localPort, remotePort, peerID, tcpServerProxy)
	return nil
}

func (s *PortForwarding) GetAllInboundStreams() []entity.InboundStream {
	res := make([]entity.InboundStream, 0)
	ids := s.conf.KnownPeersIds()
	for _, id := range ids {
		streams := s.p2p.StreamsToPeer(id)
		for _, s := range streams {
			if s.Stat().Direction == network.DirInbound {
				port := getStreamPort(s)
				if port != 0 {
					res = append(res, entity.InboundStream{
						LocalPort: port,
						PeerID:    id.Pretty(),
						Protocol:  "tcp", // TODO: udp etc
					})
				}
			}
		}
	}

	return res
}

func (s *PortForwarding) GetForwardedPorts() []entity.ForwardedPort {
	s.forwarded.RLock()
	defer s.forwarded.RUnlock()

	result := make([]entity.ForwardedPort, 0, len(s.forwarded.ports))
	for _, v := range s.forwarded.ports {
		result = append(result, entity.ForwardedPort{
			RemotePort:    v.RemotePort,
			ListenAddress: v.tcpProxy.ListenAddress(),
			PeerID:        v.PeerId.Pretty(),
		})
	}

	return result
}

func (s *PortForwarding) CloseInboundStreams(peerID peer.ID, localPort int) {
	s.closeP2pStreams(peerID, localPort, network.DirInbound)
}

func (s *PortForwarding) StopForwarding(peerID peer.ID, remotePort int) {
	s.forwarded.Lock()
	defer s.forwarded.Unlock()

	key := forwardedPort{
		RemotePort: remotePort,
		PeerId:     peerID,
	}
	portInfo, ok := s.forwarded.ports[key]
	if !ok {
		return
	}

	portInfo.tcpProxy.Close()
	s.closeP2pStreams(peerID, remotePort, network.DirOutbound)
	delete(s.forwarded.ports, key)
}

func (s *PortForwarding) stopForwardingAll(peerID peer.ID) {
	s.forwarded.Lock()
	defer s.forwarded.Unlock()

	for k, v := range s.forwarded.ports {
		if v.PeerId == peerID {
			v.tcpProxy.Close()
			delete(s.forwarded.ports, k)
		}
	}
}

func (s *PortForwarding) addForwardedPort(localPort, remotePort int, peerId peer.ID, proxy proxy.ServerProxy) {
	s.forwarded.Lock()
	defer s.forwarded.Unlock()

	key := forwardedPort{
		RemotePort: remotePort,
		PeerId:     peerId,
	}
	s.forwarded.ports[key] = forwardedPortInfo{
		LocalPort:  localPort,
		RemotePort: remotePort,
		PeerId:     peerId,
		tcpProxy:   proxy,
	}
}

func (s *PortForwarding) checkForwardedPort(peerID peer.ID, localPort, remotePort int) (handled bool, err error) {
	s.forwarded.RLock()
	defer s.forwarded.RUnlock()

	key := forwardedPort{
		RemotePort: remotePort,
		PeerId:     peerID,
	}
	portInfo, ok := s.forwarded.ports[key]
	if !ok {
		return false, nil
	}
	if portInfo.LocalPort == localPort {
		return true, nil
	}

	return false, nil
}

func (s *PortForwarding) closeP2pStreams(peerID peer.ID, targetPort int, direction network.Direction) {
	streams := s.p2p.StreamsToPeer(peerID)
	for _, s := range streams {
		if s.Stat().Direction == direction {
			port := getStreamPort(s)
			if port == targetPort {
				_ = s.Reset()
			}
		}
	}
}

func (s *PortForwarding) onPeerConnected(peerID peer.ID, _ network.Conn) {
	knownPeer, known := s.conf.GetPeer(peerID.Pretty())
	if !known {
		return
	}

	go func() {
		for _, connConfig := range knownPeer.AllowedRemotePorts {
			if connConfig.Forwarded {
				err := s.ForwardPort(peerID, knownPeer.IPAddr, connConfig.MappedLocalPort, connConfig.RemotePort)
				if err != nil {
					s.logger.Warnf("forwarding port %d from %s: %v", connConfig.RemotePort, peerID, err)
				}
			}
		}
	}()
}

func (s *PortForwarding) onPeerDisconnected(peerID peer.ID, _ network.Conn) {
	_, known := s.conf.GetPeer(peerID.Pretty())
	if !known {
		return
	}

	go func() {
		// TODO: не будет ли тут проблем во время переключения на другие сети мобильный/wifi/vpn?
		s.stopForwardingAll(peerID)
	}()
}

func setStreamPort(stream network.Stream, port int) {
	stat := stream.Stat()
	stat.Extra[portStreamKey] = port
}

func getStreamPort(stream network.Stream) int {
	extra := stream.Stat().Extra
	val, ok := extra[portStreamKey]
	if ok {
		return val.(int)
	}

	return 0
}
