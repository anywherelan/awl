package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/protocol"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
)

const (
	backgroundExchangeStatusInfoInterval = 5 * time.Minute
	backgroundRetryAuthRequests          = 5 * time.Minute
)

type AuthStatus struct {
	ingoingAuths  map[peer.ID]protocol.AuthPeer
	outgoingAuths map[peer.ID]protocol.AuthPeer
	authsLock     sync.RWMutex
	logger        *log.ZapEventLogger
	p2p           *P2pService
	conf          *config.Config
}

func NewAuthStatus(p2pService *P2pService, conf *config.Config) *AuthStatus {
	auth := &AuthStatus{
		ingoingAuths:  make(map[peer.ID]protocol.AuthPeer),
		outgoingAuths: make(map[peer.ID]protocol.AuthPeer),
		logger:        log.Logger("awl/service/status"),
		p2p:           p2pService,
		conf:          conf,
	}
	auth.restoreOutgoingAuths()
	p2pService.RegisterOnPeerConnected(auth.onPeerConnected)
	return auth
}

func (s *AuthStatus) StatusStreamHandler(stream network.Stream) {
	defer func() {
		_ = helpers.FullClose(stream)
	}()

	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	peer, known := s.conf.GetPeer(peerID)
	if !known {
		s.logger.Infof("Unknown peer %s tried to get status info", peerID)
		return
	}

	// Receiving info
	oppositePeerInfo, err := protocol.ReceiveStatus(stream)
	if err != nil {
		s.logger.Errorf("receiving status info from %s: %v", peerID, err)
		return
	}
	s.authsLock.Lock()
	delete(s.outgoingAuths, remotePeer)
	s.authsLock.Unlock()

	// Sending info
	myPeerInfo := s.createPeerInfo(peer, s.conf.P2pNode.Name)
	err = protocol.SendStatus(stream, myPeerInfo)
	if err != nil {
		s.logger.Errorf("sending status info to %s as an answer: %v", peerID, err)
	}

	// Processing opposite peer info
	newPeer := s.processPeerStatusInfo(peer, oppositePeerInfo)
	s.conf.UpsertPeer(newPeer)

	s.logger.Infof("successfully exchanged status info with %s (%s)", peer.DisplayName(), peerID)
}

// TODO: race in upserting peer config.KnownPeer: update fields separate
func (s *AuthStatus) ExchangeNewStatusInfo(remotePeerID peer.ID, peer config.KnownPeer) error {
	s.authsLock.Lock()
	delete(s.ingoingAuths, remotePeerID)
	s.authsLock.Unlock()

	err := s.p2p.ConnectPeer(context.Background(), remotePeerID)
	if err != nil {
		return err
	}

	stream, err := s.p2p.NewStream(remotePeerID, protocol.GetStatusMethod)
	if err != nil {
		return err
	}
	defer func() {
		_ = helpers.FullClose(stream)
	}()

	myPeerInfo := s.createPeerInfo(peer, s.conf.P2pNode.Name)
	err = protocol.SendStatus(stream, myPeerInfo)
	if err != nil {
		return fmt.Errorf("sending status info: %v", err)
	}

	oppositePeerInfo, err := protocol.ReceiveStatus(stream)
	if err != nil {
		return fmt.Errorf("receiving status info: %v", err)
	}

	newPeer := s.processPeerStatusInfo(peer, oppositePeerInfo)
	s.conf.UpsertPeer(newPeer)

	return nil
}

func (*AuthStatus) createPeerInfo(_ config.KnownPeer, name string) protocol.PeerStatusInfo {
	myPeerInfo := protocol.PeerStatusInfo{
		Name: name,
	}

	return myPeerInfo
}

func (*AuthStatus) processPeerStatusInfo(peer config.KnownPeer, peerInfo protocol.PeerStatusInfo) config.KnownPeer {
	peer.Name = peerInfo.Name
	peer.Confirmed = true
	peer.LastSeen = time.Now()
	return peer
}

func (s *AuthStatus) AuthStreamHandler(stream network.Stream) {
	defer func() {
		_ = helpers.FullClose(stream)
	}()

	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	authPeer, err := protocol.ReceiveAuth(stream)
	if err != nil {
		s.logger.Errorf("receiving auth from %s: %v", peerID, err)
		return
	}

	_, confirmed := s.conf.GetPeer(peerID)
	if !confirmed {
		s.authsLock.Lock()
		s.ingoingAuths[remotePeer] = authPeer
		s.authsLock.Unlock()
	}

	err = protocol.SendAuthResponse(stream, protocol.AuthPeerResponse{Confirmed: confirmed})
	if err != nil {
		s.logger.Errorf("sending auth response to %s as an answer: %v", peerID, err)
		return
	}

	s.logger.Infof("Successfully received auth from %s (%s)", authPeer.Name, peerID)
}

func (s *AuthStatus) SendAuthRequest(peerID peer.ID, req protocol.AuthPeer) error {
	s.authsLock.Lock()
	s.outgoingAuths[peerID] = req
	s.authsLock.Unlock()

	err := s.p2p.ConnectPeer(context.Background(), peerID)
	if err != nil {
		return err
	}

	stream, err := s.p2p.NewStream(peerID, protocol.AuthMethod)
	if err != nil {
		return err
	}
	defer func() {
		_ = helpers.FullClose(stream)
	}()

	err = protocol.SendAuth(stream, req)
	if err != nil {
		return fmt.Errorf("sending auth: %v", err)
	}

	authResponse, err := protocol.ReceiveAuthResponse(stream)
	if err != nil {
		return fmt.Errorf("receiving auth response from %s: %v", peerID, err)
	}

	if authResponse.Confirmed {
		s.authsLock.Lock()
		delete(s.outgoingAuths, peerID)
		s.authsLock.Unlock()
	}

	s.logger.Infof("Successfully send auth to %s", peerID)
	return nil
}

func (s *AuthStatus) BackgroundRetryAuthRequests() {
	f := func() {
		for peerID, auth := range s.outgoingAuths {
			_ = s.SendAuthRequest(peerID, auth)
			//if err != nil {
			//	s.logger.Warnf("retry auth to %s: %v", peerIDStr, err)
			//}
		}
	}

	t := time.NewTicker(backgroundRetryAuthRequests)

	for range t.C {
		f()
	}
}

// TODO: сделать так, чтобы отправляла статус только одна из сторон. Например, та, которая подключилась.
//  Затем можно еще уменьшить интервал
func (s *AuthStatus) BackgroundExchangeStatusInfo() {
	f := func() {
		for _, knownPeer := range s.conf.KnownPeers {
			//if !knownPeer.Confirmed {
			//	continue
			//}

			peerID := knownPeer.PeerId()
			if peerID == "" {
				continue
			}
			_ = s.ExchangeNewStatusInfo(peerID, knownPeer)
		}
	}

	ticker := time.NewTicker(backgroundExchangeStatusInfoInterval)

	for range ticker.C {
		f()
	}
}

func (s *AuthStatus) GetIngoingAuthRequests() map[string]protocol.AuthPeer {
	s.authsLock.RLock()
	defer s.authsLock.RUnlock()

	result := make(map[string]protocol.AuthPeer, len(s.ingoingAuths))
	for peerID, auth := range s.ingoingAuths {
		result[peerID.String()] = auth
	}
	return result
}

func (s *AuthStatus) restoreOutgoingAuths() {
	s.conf.RLock()
	defer s.conf.RUnlock()

	peerName := s.conf.P2pNode.Name
	outgoingAuths := make(map[peer.ID]protocol.AuthPeer)
	for _, knownPeer := range s.conf.KnownPeers {
		if !knownPeer.Confirmed {
			outgoingAuths[knownPeer.PeerId()] = protocol.AuthPeer{
				Name: peerName,
			}
		}
	}
	s.outgoingAuths = outgoingAuths
}

func (s *AuthStatus) onPeerConnected(peerID peer.ID, conn network.Conn) {
	s.authsLock.RLock()
	authPeer, hasOutgAuth := s.outgoingAuths[peerID]
	s.authsLock.RUnlock()

	knownPeer, known := s.conf.GetPeer(peerID.Pretty())

	if !known && !hasOutgAuth {
		return
	}

	go func() {
		if hasOutgAuth {
			err := s.SendAuthRequest(peerID, authPeer)
			if err != nil {
				s.logger.Errorf("send auth to recently connected peer %s: %v", peerID, err)
			}
		}

		if known {
			var dir string
			switch conn.Stat().Direction {
			case network.DirOutbound:
				dir = "outbound"
			case network.DirInbound:
				dir = "inbound"
			case network.DirUnknown:
				dir = "unknown"
			}
			s.logger.Infof("peer %s connected, direction %s, address %s", knownPeer.DisplayName(), dir, conn.RemoteMultiaddr())

			err := s.ExchangeNewStatusInfo(peerID, knownPeer)
			if err != nil && knownPeer.Confirmed {
				s.logger.Errorf("exchange status info with recently connected peer %s (%s): %v", knownPeer.DisplayName(), peerID, err)
			}
		}
	}()
}
