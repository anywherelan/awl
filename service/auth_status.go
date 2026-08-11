package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pProtocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/metrics"
	"github.com/anywherelan/awl/protocol"
)

const (
	backgroundExchangeStatusInfoInterval = 5 * time.Minute
	backgroundRetryAuthRequests          = 5 * time.Minute
)

type P2p interface {
	ConnectPeer(ctx context.Context, peerID peer.ID) error
	IsConnected(peerID peer.ID) bool
	NewStream(ctx context.Context, id peer.ID, proto libp2pProtocol.ID) (network.Stream, error)
	NewStreamMulti(ctx context.Context, id peer.ID, protos ...libp2pProtocol.ID) (network.Stream, error)
	NewStreamWithDedicatedConn(ctx context.Context, id peer.ID, proto libp2pProtocol.ID) (network.Stream, error)
	SubscribeConnectionEvents(onConnected, onDisconnected func(network.Network, network.Conn))
	RecordPeerLatency(id peer.ID, rtt time.Duration)
}

type AuthStatus struct {
	ingoingAuths  map[peer.ID]protocol.AuthPeer
	outgoingAuths map[peer.ID]protocol.AuthPeer
	authsLock     sync.RWMutex
	logger        *log.ZapEventLogger
	p2p           P2p
	conf          *config.Config
	authsEmitter  awlevent.Emitter
	inviteEmitter awlevent.Emitter
}

func NewAuthStatus(p2pService P2p, conf *config.Config, eventbus awlevent.Bus) *AuthStatus {
	emitter, err := eventbus.Emitter(new(awlevent.ReceivedAuthRequest))
	if err != nil {
		panic(err)
	}
	inviteEmitter, err := eventbus.Emitter(new(awlevent.InviteRedeemed))
	if err != nil {
		panic(err)
	}

	auth := &AuthStatus{
		ingoingAuths:  make(map[peer.ID]protocol.AuthPeer),
		outgoingAuths: make(map[peer.ID]protocol.AuthPeer),
		logger:        log.Logger("awl/service/status"),
		p2p:           p2pService,
		conf:          conf,
		authsEmitter:  emitter,
		inviteEmitter: inviteEmitter,
	}
	auth.restoreOutgoingAuths()
	p2pService.SubscribeConnectionEvents(auth.onPeerConnected, auth.onPeerDisconnected)
	return auth
}

func (s *AuthStatus) StatusStreamHandler(stream network.Stream) {
	metrics.PeersStatusRequestsReceivedTotal.Inc()
	defer func() {
		_ = stream.Close()
	}()

	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	knownPeer, known := s.conf.GetPeer(peerID)
	_, isBlocked := s.conf.GetBlockedPeer(peerID)
	if !known && !isBlocked {
		s.logger.Infof("Unknown peer %s tried to exchange status info", peerID)
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
	myPeerInfo := s.createPeerInfo(knownPeer, s.conf.NodeName(), isBlocked)
	err = protocol.SendStatus(stream, myPeerInfo)
	if err != nil {
		s.logger.Errorf("sending status info to %s as an answer: %v", peerID, err)
	}

	s.logger.Infof("successfully exchanged status info (inbound) with %s (%s)", knownPeer.DisplayName(), peerID)
	if isBlocked {
		return
	}

	s.processPeerStatusInfo(peerID, oppositePeerInfo)
}

func (s *AuthStatus) ExchangeNewStatusInfo(ctx context.Context, remotePeerID peer.ID, knownPeer config.KnownPeer) error {
	s.authsLock.Lock()
	delete(s.ingoingAuths, remotePeerID)
	s.authsLock.Unlock()

	err := s.p2p.ConnectPeer(ctx, remotePeerID)
	if err != nil {
		return err
	}

	stream, err := s.p2p.NewStream(ctx, remotePeerID, protocol.GetStatusMethod)
	if err != nil {
		return err
	}
	defer func() {
		_ = stream.Close()
	}()

	metrics.PeersStatusRequestsSentTotal.Inc()

	_, isBlocked := s.conf.GetBlockedPeer(remotePeerID.String())
	myPeerInfo := s.createPeerInfo(knownPeer, s.conf.NodeName(), isBlocked)
	timeStarted := time.Now()
	err = protocol.SendStatus(stream, myPeerInfo)
	if err != nil {
		return fmt.Errorf("sending status info: %v", err)
	}

	oppositePeerInfo, err := protocol.ReceiveStatus(stream)
	if err != nil {
		return fmt.Errorf("receiving status info: %v", err)
	}
	s.p2p.RecordPeerLatency(remotePeerID, time.Since(timeStarted))

	s.logger.Infof("successfully exchanged status info (outbound) with %s (%s)", knownPeer.DisplayName(), remotePeerID.String())
	if isBlocked {
		return nil
	}

	s.processPeerStatusInfo(remotePeerID.String(), oppositePeerInfo)

	return nil
}

func (s *AuthStatus) BlockPeer(peerID peer.ID, name string) {
	s.conf.UpsertBlockedPeer(peerID.String(), name)

	// Stop retrying our own auth request to a peer we just blocked or removed.
	s.authsLock.Lock()
	delete(s.outgoingAuths, peerID)
	s.authsLock.Unlock()

	go func() {
		_ = s.ExchangeNewStatusInfo(context.Background(), peerID, config.KnownPeer{})
	}()
}

func (s *AuthStatus) createPeerInfo(peer config.KnownPeer, myPeerName string, declined bool) protocol.PeerStatusInfo {
	if declined {
		return protocol.PeerStatusInfo{
			Declined: true,
		}
	}
	s.conf.RLock()
	vpnGatewayServerEnabled := s.conf.VPNGateway.ServerEnabled
	s.conf.RUnlock()

	var ipv6Addr string
	if ipV6, _ := s.conf.VPNLocalIPMaskV6(); ipV6 != nil {
		ipv6Addr = ipV6.String()
	}

	return protocol.PeerStatusInfo{
		Name:                    myPeerName,
		AllowUsingAsExitNode:    peer.WeAllowUsingAsExitNode,
		VPNGatewayServerEnabled: vpnGatewayServerEnabled,
		IPv6Addr:                ipv6Addr,
	}
}

// processPeerStatusInfo merges the status info received from peerID into the
// stored KnownPeer. It touches only the status-owned fields via UpdatePeerFields
// so that a concurrent settings update (e.g. IPAddr) is not clobbered. No-op if
// the peer is no longer known.
func (s *AuthStatus) processPeerStatusInfo(peerID string, peerInfo protocol.PeerStatusInfo) {
	if peerInfo.Declined {
		// TODO: emit an awlevent (analogous to VPNGatewayConnectivityChanged) when
		//  a peer explicitly declines us, so the UI can surface it immediately. The
		//  peer stays in KnownPeers as not-confirmed (including if it was our VPN
		//  gateway — we keep the binding even though it will no longer forward).
		s.conf.UpdatePeerFields(peerID, func(peer *config.KnownPeer) {
			peer.LastSeen = time.Now()
			peer.Declined = true
			peer.PendingInviteToken = ""
		})

		s.clearSelectedExitNode(peerID)

		return
	}

	s.conf.UpdatePeerFields(peerID, func(peer *config.KnownPeer) {
		peer.LastSeen = time.Now()
		peer.Name = peerInfo.Name
		peer.Confirmed = true
		peer.Declined = false
		// The invite token has done its job once the peer confirms us
		peer.PendingInviteToken = ""
		if peer.DomainName == "" {
			peer.DomainName = awldns.TrimDomainName(peer.DisplayName())
		}
		if peer.Alias == "" {
			peer.Alias = s.conf.GenUniqPeerAliasUnlocked(peer.Name, peer.Alias)
		}
		if peerInfo.IPv6Addr != "" && peer.IPAddrV6 == "" {
			peer.IPAddrV6 = peerInfo.IPv6Addr
		}
		peer.AllowedUsingAsExitNode = peerInfo.AllowUsingAsExitNode
		peer.RemoteVPNGatewayServerEnabled = peerInfo.VPNGatewayServerEnabled
	})

	if !peerInfo.AllowUsingAsExitNode {
		s.clearSelectedExitNode(peerID)
	}
}

// clearSelectedExitNode drops peerID as our SOCKS5 exit node if it is the one
// currently selected. Called when the peer declines us or revokes the
// permission: we never select an exit node on the peer's behalf, but we do stop
// using one that no longer allows it.
func (s *AuthStatus) clearSelectedExitNode(peerID string) {
	s.conf.Lock()
	defer s.conf.Unlock()

	if s.conf.SOCKS5.UsingPeerID != peerID {
		return
	}
	s.conf.SOCKS5.UsingPeerID = ""
	s.conf.Save()
}

func (s *AuthStatus) AuthStreamHandler(stream network.Stream) {
	metrics.PeersAuthRequestsReceivedTotal.Inc()
	defer func() {
		_ = stream.Close()
	}()

	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	authPeer, err := protocol.ReceiveAuth(stream)
	if err != nil {
		s.logger.Errorf("receiving auth from %s: %v", peerID, err)
		return
	}

	// The invite token is a bearer secret: it is consumed here and dropped, so
	// that it is never stored in ingoingAuths, emitted, or returned by API.
	inviteToken := authPeer.Token
	authPeer.Token = ""

	_, isBlocked := s.conf.GetBlockedPeer(peerID)
	_, known := s.conf.GetPeer(peerID)
	s.conf.RLock()
	autoAccept := s.conf.P2pNode.AutoAcceptAuthRequests
	s.conf.RUnlock()

	// An invite only ever lets a new peer in.
	// A peer already in KnownPeers is kept as is, we don't apply settings from the invite to it.
	// Blocking outranks an invite.
	var (
		invite      config.Invite
		inviteValid bool
	)
	if !isBlocked && !known {
		invite, inviteValid = s.checkInvite(inviteToken, peerID)
	}

	if !isBlocked && !known && (inviteValid || autoAccept) {
		// A false here means the add lost a race for the invite.
		known = s.addPeerFromAuth(remotePeer, authPeer, invite, inviteValid)
	}

	// Whoever we did not take in above is offered for a manual accept.
	// This is the ordinary path for a peer with no invite and no auto-accept, and the
	// fallback for an auto-add that lost a race.
	if !isBlocked && !known {
		s.authsLock.Lock()
		s.ingoingAuths[remotePeer] = authPeer
		s.authsLock.Unlock()
		_ = s.authsEmitter.Emit(awlevent.ReceivedAuthRequest{
			AuthPeer: authPeer,
			PeerID:   peerID,
		})
	}

	authResponse := protocol.AuthPeerResponse{Confirmed: known, Declined: isBlocked}
	err = protocol.SendAuthResponse(stream, authResponse)
	if err != nil {
		s.logger.Errorf("sending auth response to %s as an answer: %v", peerID, err)
		return
	}

	s.logger.Infof("Successfully received auth from %s (%s)", authPeer.Name, peerID)
}

// checkInvite looks up a token from an auth request, without consuming anything.
func (s *AuthStatus) checkInvite(token, peerID string) (config.Invite, bool) {
	if token == "" {
		return config.Invite{}, false
	}

	invite, err := s.conf.CheckInvite(token)
	if err != nil {
		if reason := inviteRejectionReason(err); reason != "" {
			metrics.InviteTokensRejectedTotal.WithLabelValues(reason).Inc()
		}
		s.logger.Infof("peer %s presented an unusable invite token: %v", peerID, err)
		return config.Invite{}, false
	}

	return invite, true
}

func inviteRejectionReason(err error) string {
	switch {
	case errors.Is(err, config.ErrInviteExpired):
		return "expired"
	case errors.Is(err, config.ErrInviteRevoked):
		return "revoked"
	case errors.Is(err, config.ErrInviteUsedUp):
		return "used_up"
	case errors.Is(err, config.ErrInviteNotFound):
		return "unknown"
	}
	return ""
}

// addPeerFromAuth adds a peer that arrived with a valid invite token or under AutoAcceptAuthRequests.
// A valid token has a priority and applies the invite's settings.
func (s *AuthStatus) addPeerFromAuth(remotePeer peer.ID, authPeer protocol.AuthPeer, invite config.Invite, inviteValid bool) bool {
	params := AddPeerParams{
		PeerID:        remotePeer,
		Name:          authPeer.Name,
		Confirmed:     true,
		UniquifyAlias: true,
	}
	if inviteValid {
		// The invite's own settings are read by AddPeer, from the invite it
		// redeems — not copied from the check above, which is only advisory.
		params.RedeemInviteToken = invite.Token
	}

	err := s.AddPeer(context.Background(), params)
	if err != nil {
		// Also covers the invite going unusable between the check above and the
		// redemption inside AddPeer (two holders of a single-use link arriving together).
		if reason := inviteRejectionReason(err); reason != "" {
			metrics.InviteTokensRejectedTotal.WithLabelValues(reason).Inc()
		}
		s.logger.Errorf("failed to auto-add peer %s (from invite: %v) %s: %v", params.Name, inviteValid, params.PeerID, err)
		return false
	}

	if inviteValid {
		s.onInviteRedeemed(remotePeer.String(), params.Name, invite.ID)
	}

	return true
}

func (s *AuthStatus) onInviteRedeemed(peerID, peerName, inviteID string) {
	metrics.InvitesRedeemedTotal.Inc()
	s.logger.Infof("peer %s %s was added automatically through invite %s", peerName, peerID, inviteID)
	// TODO: surface this in the UI, or at least as a tray notification
	_ = s.inviteEmitter.Emit(awlevent.InviteRedeemed{
		PeerID:   peerID,
		InviteID: inviteID,
	})
}

func (s *AuthStatus) SendAuthRequest(ctx context.Context, peerID peer.ID, req protocol.AuthPeer) error {
	metrics.PeersAuthRequestsSentTotal.Inc()
	s.authsLock.Lock()
	s.outgoingAuths[peerID] = req
	s.authsLock.Unlock()

	err := s.p2p.ConnectPeer(ctx, peerID)
	if err != nil {
		return err
	}

	stream, err := s.p2p.NewStream(ctx, peerID, protocol.AuthMethod)
	if err != nil {
		return err
	}
	defer func() {
		_ = stream.Close()
	}()

	timeStarted := time.Now()
	err = protocol.SendAuth(stream, req)
	if err != nil {
		return fmt.Errorf("sending auth: %v", err)
	}

	authResponse, err := protocol.ReceiveAuthResponse(stream)
	if err != nil {
		return fmt.Errorf("receiving auth response from %s: %v", peerID, err)
	}
	s.p2p.RecordPeerLatency(peerID, time.Since(timeStarted))

	if authResponse.Confirmed || authResponse.Declined {
		s.authsLock.Lock()
		delete(s.outgoingAuths, peerID)
		s.authsLock.Unlock()
	}
	if authResponse.Declined {
		s.conf.UpdatePeerFields(peerID.String(), func(peer *config.KnownPeer) {
			peer.Declined = true
		})
	}

	s.logger.Infof("Successfully send auth to %s", peerID)
	return nil
}

// AddPeerParams describes a peer about to be added to KnownPeers. It is a
// struct rather than a positional argument list because the callers set
// different subsets of it (an outgoing invite, an accepted request, an
// auto-accepted one), and because it keeps growing.
type AddPeerParams struct {
	PeerID peer.ID
	// Name is how the peer calls itself; empty when we invite it first and
	// learn the name later from the status exchange.
	Name string
	// Alias is the local name for the peer, chosen by us.
	Alias string
	// UniquifyAlias makes a clashing (or empty) Alias be resolved into a free
	// one instead of failing, under the same lock that inserts the peer. Set it
	// on paths with no user to report a clash to; a user-driven add leaves it
	// false so the error reaches the form that caused it.
	UniquifyAlias bool
	// Confirmed reports whether the peer has already confirmed us, i.e. we are
	// accepting its request rather than sending our own.
	Confirmed bool
	// IPAddr is the VPN address for the peer. Generated when empty.
	IPAddr string
	// AllowUsingAsExitNode lets the peer use us as a SOCKS5 / VPN Gateway exit node.
	// Announced to it by the status exchange, see createPeerInfo.
	AllowUsingAsExitNode bool
	// RedeemInviteToken is a token of OUR invite, presented to us by the peer.
	// AddPeer spends one use of the matching invite and takes Alias,
	// AllowUsingAsExitNode and InviteID from it, overriding whatever was passed here.
	RedeemInviteToken string
	// PendingInviteToken is the invite token we present to the peer until it
	// confirms us. Set on the side that redeems a link.
	PendingInviteToken string
}

func (s *AuthStatus) AddPeer(ctx context.Context, params AddPeerParams) error {
	peerID := params.PeerID
	peerIDStr := peerID.String()
	alias := strings.TrimSpace(params.Alias)

	s.conf.RemoveBlockedPeer(peerIDStr)

	s.conf.Lock()
	if _, exist := s.conf.GetPeerUnlocked(peerIDStr); exist {
		s.conf.Unlock()
		return fmt.Errorf("peer has already been added")
	}
	inviteID := ""
	if params.RedeemInviteToken != "" {
		invite, err := s.conf.ReserveInviteUnlocked(params.RedeemInviteToken)
		if err != nil {
			s.conf.Unlock()
			return fmt.Errorf("redeeming invite: %w", err)
		}
		alias = strings.TrimSpace(invite.Alias)
		params.AllowUsingAsExitNode = invite.WeAllowUsingAsExitNode
		inviteID = invite.ID
	}
	if params.UniquifyAlias {
		alias = s.conf.GenUniqPeerAliasUnlocked(params.Name, alias)
	} else if !s.conf.IsUniqPeerAliasUnlocked("", alias) {
		s.conf.Unlock()
		return fmt.Errorf("peer name is not unique")
	}
	ipAddr := params.IPAddr
	if ipAddr != "" {
		if err := s.conf.CheckIPUnique(ipAddr, peerIDStr); err != nil {
			s.conf.Unlock()
			return err
		}
	} else {
		ipAddr = s.conf.GenerateNextIpAddr()
	}

	newPeerConfig := config.KnownPeer{
		PeerID:                 peerIDStr,
		Name:                   params.Name,
		Alias:                  alias,
		IPAddr:                 ipAddr,
		Confirmed:              params.Confirmed,
		CreatedAt:              time.Now(),
		WeAllowUsingAsExitNode: params.AllowUsingAsExitNode,
		InviteID:               inviteID,
		PendingInviteToken:     params.PendingInviteToken,
	}
	newPeerConfig.DomainName = awldns.TrimDomainName(newPeerConfig.DisplayName())
	s.conf.UpsertPeerUnlocked(newPeerConfig)
	s.conf.Unlock()

	// Drop any pending incoming auth request synchronously, so the caller
	// (e.g. AcceptFriend) observes it cleared on return rather than after the
	// asynchronous status exchange below happens to run.
	s.authsLock.Lock()
	delete(s.ingoingAuths, peerID)
	s.authsLock.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if !params.Confirmed {
			authPeer := protocol.AuthPeer{
				Name:  s.conf.NodeName(),
				Token: params.PendingInviteToken,
			}
			_ = s.SendAuthRequest(ctx, peerID, authPeer)
		}

		knownPeer, _ := s.conf.GetPeer(peerIDStr)
		_ = s.ExchangeNewStatusInfo(ctx, peerID, knownPeer)
	}()

	return nil
}

func (s *AuthStatus) ExchangeStatusInfoWithAllKnownPeers(ctx context.Context) {
	s.conf.RLock()
	peers := make([]string, 0, len(s.conf.KnownPeers))
	for peerID := range s.conf.KnownPeers {
		peers = append(peers, peerID)
	}
	s.conf.RUnlock()

	for _, peerID := range peers {
		knownPeer, exists := s.conf.GetPeer(peerID)
		if !exists {
			continue
		}
		_ = s.ExchangeNewStatusInfo(ctx, knownPeer.PeerId(), knownPeer)
	}
}

func (s *AuthStatus) BackgroundRetryAuthRequests(ctx context.Context) {
	f := func() {
		s.authsLock.RLock()
		outgoingAuthsCopy := maps.Clone(s.outgoingAuths)
		s.authsLock.RUnlock()

		for peerID, auth := range outgoingAuthsCopy {
			_ = s.SendAuthRequest(ctx, peerID, auth)
		}
	}

	ticker := time.NewTicker(backgroundRetryAuthRequests)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f()
			ticker.Reset(backgroundRetryAuthRequests)
		}
	}
}

func (s *AuthStatus) BackgroundExchangeStatusInfo(ctx context.Context) {
	ticker := time.NewTicker(backgroundExchangeStatusInfoInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ExchangeStatusInfoWithAllKnownPeers(ctx)
			ticker.Reset(backgroundExchangeStatusInfoInterval)
		}
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
		if !knownPeer.Confirmed && !knownPeer.Declined {
			outgoingAuths[knownPeer.PeerId()] = protocol.AuthPeer{
				Name:  peerName,
				Token: knownPeer.PendingInviteToken,
			}
		}
	}
	s.outgoingAuths = outgoingAuths
}

func (s *AuthStatus) onPeerConnected(_ network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	s.authsLock.RLock()
	authPeer, hasOutgAuth := s.outgoingAuths[peerID]
	s.authsLock.RUnlock()

	knownPeer, known := s.conf.GetPeer(peerID.String())
	if !known && !hasOutgAuth {
		return
	}
	s.conf.UpdatePeerLastSeen(peerID.String())

	go func() {
		if hasOutgAuth {
			err := s.SendAuthRequest(context.Background(), peerID, authPeer)
			if err != nil {
				s.logger.Errorf("send auth to recently connected peer %s: %v", peerID, err)
			}
		}

		if known {
			metrics.PeersConnectionEventsTotal.WithLabelValues("connected").Inc()
			dir := strings.ToLower(conn.Stat().Direction.String())
			s.logger.Infof("peer '%s' connected, direction %s, address %s", knownPeer.DisplayName(), dir, conn.RemoteMultiaddr())

			err := s.ExchangeNewStatusInfo(context.Background(), peerID, knownPeer)
			if err != nil && knownPeer.Confirmed {
				s.logger.Errorf("exchange status info with recently connected peer %s (%s): %v", knownPeer.DisplayName(), peerID, err)
			}
		}
	}()
}

func (s *AuthStatus) onPeerDisconnected(_ network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	knownPeer, known := s.conf.GetPeer(peerID.String())
	if !known {
		return
	}
	metrics.PeersConnectionEventsTotal.WithLabelValues("disconnected").Inc()
	s.conf.UpdatePeerLastSeen(peerID.String())
	s.logger.Infof("peer '%s' disconnected, address %s", knownPeer.DisplayName(), conn.RemoteMultiaddr())
}

// GetAuthRequestCounts returns the number of pending ingoing and outgoing auth requests.
func (s *AuthStatus) GetAuthRequestCounts() (ingoing, outgoing int) {
	s.authsLock.RLock()
	defer s.authsLock.RUnlock()
	return len(s.ingoingAuths), len(s.outgoingAuths)
}
