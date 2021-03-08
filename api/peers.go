package api

import (
	"net/http"
	"sort"

	"github.com/labstack/echo/v4"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/peerlan/peerlan/config"
	"github.com/peerlan/peerlan/entity"
	"github.com/peerlan/peerlan/protocol"
)

// @Tags Peers
// @Summary Get known peers info
// @Accept json
// @Produce json
// @Success 200 {array} entity.KnownPeersResponse
// @Router /peers/get_known [GET]
func (h *Handler) GetKnownPeers(c echo.Context) (err error) {
	result := make([]entity.KnownPeersResponse, 0, len(h.conf.KnownPeers))

	peers := make([]string, 0, len(h.conf.KnownPeers))
	for peerID := range h.conf.KnownPeers {
		peers = append(peers, peerID)
	}
	sort.Strings(peers)

	for _, peerID := range peers {
		peer := h.conf.KnownPeers[peerID]

		remotePorts := make([]int, 0, len(peer.AllowedRemotePorts))
		for port := range peer.AllowedRemotePorts {
			remotePorts = append(remotePorts, port)
		}
		sort.Ints(remotePorts)
		localPorts := make([]int, 0, len(peer.AllowedLocalPorts))
		for port := range peer.AllowedLocalPorts {
			localPorts = append(localPorts, port)
		}
		sort.Ints(localPorts)

		id := peer.PeerId()
		kpr := entity.KnownPeersResponse{
			PeerID:             peerID,
			Name:               peer.DisplayName(),
			Version:            h.p2p.PeerVersion(id),
			IpAddr:             peer.IPAddr,
			Connected:          h.p2p.IsConnected(id),
			Confirmed:          peer.Confirmed,
			LastSeen:           peer.LastSeen,
			Addresses:          h.p2p.PeerAddresses(id),
			NetworkStats:       h.p2p.NetworkStatsForPeer(id),
			AllowedLocalPorts:  localPorts,
			AllowedRemotePorts: remotePorts,
		}
		result = append(result, kpr)
	}
	return c.JSON(http.StatusOK, result)
}

// @Tags Peers
// @Summary Get known peer settings
// @Accept json
// @Produce json
// @Param body body entity.PeerIDRequest true "Params"
// @Success 200 {object} config.KnownPeer
// @Failure 400 {object} api.Error
// @Failure 404 {object} api.Error
// @Router /peers/get_known_peer_settings [GET]
func (h *Handler) GetKnownPeerSettings(c echo.Context) (err error) {
	req := entity.PeerIDRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	peer, exists := h.conf.GetPeer(req.PeerID)
	if !exists {
		return c.JSON(http.StatusNotFound, ErrorMessage("peer not found"))
	}

	return c.JSON(http.StatusOK, peer)
}

// @Tags Peers
// @Summary Update peer settings
// @Accept json
// @Produce json
// @Param body body entity.UpdatePeerSettingsRequest true "Params"
// @Success 200 "OK"
// @Failure 400 {object} api.Error
// @Failure 404 {object} api.Error
// @Router /peers/update_settings [GET]
func (h *Handler) UpdatePeerSettings(c echo.Context) (err error) {
	req := entity.UpdatePeerSettingsRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	knownPeer, exists := h.conf.GetPeer(req.PeerID)
	if !exists {
		return c.JSON(http.StatusNotFound, ErrorMessage("peer not found"))
	}
	peerID := knownPeer.PeerId()

	for remotePort := range knownPeer.AllowedRemotePorts {
		oldConf := knownPeer.AllowedRemotePorts[remotePort]
		newConf, exists := req.RemoteConns[remotePort]
		if !exists {
			continue
		}
		newConf.RemotePort = oldConf.RemotePort
		newConf.Description = oldConf.Description
		knownPeer.AllowedRemotePorts[remotePort] = newConf

		// TODO: check if mapped port is open, is it used at other ports, etc
		if newConf.Forwarded {
			go func() {
				if oldConf.MappedLocalPort != newConf.MappedLocalPort {
					h.forwarding.StopForwarding(peerID, remotePort)
				}
				err := h.forwarding.ForwardPort(peerID, knownPeer.IPAddr, newConf.MappedLocalPort, newConf.RemotePort)
				if err != nil {
					h.logger.Warnf("forwarding port %d from %s: %v", newConf.RemotePort, req.PeerID, err)
				}
			}()
		} else {
			go h.forwarding.StopForwarding(peerID, remotePort)
		}
	}

	knownPeer.AllowedLocalPorts = req.LocalConns
	knownPeer.Alias = req.Alias
	h.conf.UpsertPeer(knownPeer)

	go func() {
		for port := range knownPeer.AllowedLocalPorts {
			_, exists := req.RemoteConns[port]
			if !exists {
				h.forwarding.CloseInboundStreams(peerID, port)
			}
		}
		_ = h.authStatus.ExchangeNewStatusInfo(peerID, knownPeer)
	}()

	return c.NoContent(http.StatusOK)
}

// @Tags Peers
// @Summary Invite new peer
// @Accept json
// @Produce json
// @Param body body entity.FriendRequest true "Params"
// @Success 200 "OK"
// @Failure 400 {object} api.Error
// @Failure 500 {object} api.Error
// @Router /peers/invite_peer [POST]
func (h *Handler) SendFriendRequest(c echo.Context) (err error) {
	req := entity.FriendRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	peerId, err := peer.Decode(req.PeerID)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			ErrorMessage("Invalid hex-encoded multihash representing of a peer ID"))
	}

	if req.PeerID == h.conf.P2pNode.PeerID {
		return c.JSON(http.StatusBadRequest,
			ErrorMessage("You can't add yourself"))
	}

	_, exist := h.conf.GetPeer(req.PeerID)
	if exist {
		return c.JSON(http.StatusBadRequest, ErrorMessage("Peer has already been added"))
	}

	h.conf.RLock()
	ipAddr := h.conf.GenerateNextIpAddr()
	h.conf.RUnlock()
	newPeerConfig := config.KnownPeer{
		PeerID:             req.PeerID,
		Name:               "",
		Alias:              req.Alias,
		IPAddr:             ipAddr,
		AllowedLocalPorts:  make(map[int]config.LocalConnConfig),
		AllowedRemotePorts: make(map[int]config.RemoteConnConfig),
		Confirmed:          false,
	}
	h.conf.UpsertPeer(newPeerConfig)
	h.p2p.ProtectPeer(peerId)

	go func() {
		authPeer := protocol.AuthPeer{
			Name: h.conf.P2pNode.Name,
		}
		_ = h.authStatus.SendAuthRequest(peerId, authPeer)
	}()

	return c.NoContent(http.StatusOK)
}

// @Tags Peers
// @Summary Accept new peer's invitation
// @Accept json
// @Produce json
// @Param body body entity.FriendRequest true "Params"
// @Success 200 "OK"
// @Failure 400 {object} api.Error
// @Failure 500 {object} api.Error
// @Router /peers/accept_peer [POST]
func (h *Handler) AcceptFriend(c echo.Context) (err error) {
	req := entity.FriendRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	peerId, err := peer.Decode(req.PeerID)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			ErrorMessage("Invalid hex-encoded multihash representing of a peer ID"))
	}

	if req.PeerID == h.conf.P2pNode.PeerID {
		return c.JSON(http.StatusBadRequest,
			ErrorMessage("You can't add yourself"))
	}

	_, exist := h.conf.GetPeer(req.PeerID)
	if exist {
		return c.JSON(http.StatusBadRequest, ErrorMessage("Peer has been already added"))
	}

	authRequestsMap := h.authStatus.GetIngoingAuthRequests()
	auth, exist := authRequestsMap[req.PeerID]
	if !exist {
		return c.JSON(http.StatusBadRequest, ErrorMessage("Peer did not send you friend request"))
	}

	h.conf.RLock()
	ipAddr := h.conf.GenerateNextIpAddr()
	h.conf.RUnlock()
	newPeerConfig := config.KnownPeer{
		PeerID:             req.PeerID,
		Name:               auth.Name,
		Alias:              req.Alias,
		IPAddr:             ipAddr,
		AllowedLocalPorts:  make(map[int]config.LocalConnConfig),
		AllowedRemotePorts: make(map[int]config.RemoteConnConfig),
		Confirmed:          true,
	}
	h.conf.UpsertPeer(newPeerConfig)
	h.p2p.ProtectPeer(peerId)

	go func() {
		_ = h.authStatus.ExchangeNewStatusInfo(peerId, newPeerConfig)
	}()

	return c.NoContent(http.StatusOK)
}

// @Tags Peers
// @Summary Get ingoing auth requests
// @Accept json
// @Produce json
// @Success 200 {array} entity.AuthRequest
// @Router /peers/auth_requests [GET]
func (h *Handler) GetAuthRequests(c echo.Context) (err error) {
	authRequestsMap := h.authStatus.GetIngoingAuthRequests()
	authRequests := make([]entity.AuthRequest, 0, len(authRequestsMap))
	for peerID, req := range authRequestsMap {
		authRequests = append(authRequests, entity.AuthRequest{
			AuthPeer: req,
			PeerID:   peerID,
		})
	}
	return c.JSON(http.StatusOK, authRequests)
}
