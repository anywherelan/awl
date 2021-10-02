package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/anywherelan/awl/protocol"
	"github.com/labstack/echo/v4"
	"github.com/libp2p/go-libp2p-core/peer"
)

// @Tags Peers
// @Summary Get known peers info
// @Accept json
// @Produce json
// @Success 200 {array} entity.KnownPeersResponse
// @Router /peers/get_known [GET]
func (h *Handler) GetKnownPeers(c echo.Context) (err error) {
	result := make([]entity.KnownPeersResponse, 0, len(h.conf.KnownPeers))

	h.conf.RLock()
	peers := make([]string, 0, len(h.conf.KnownPeers))
	for peerID := range h.conf.KnownPeers {
		peers = append(peers, peerID)
	}
	h.conf.RUnlock()
	sort.Strings(peers)

	for _, peerID := range peers {
		knownPeer, _ := h.conf.GetPeer(peerID)

		id := knownPeer.PeerId()
		kpr := entity.KnownPeersResponse{
			PeerID:       peerID,
			Name:         knownPeer.DisplayName(),
			Version:      h.p2p.PeerVersion(id),
			IpAddr:       knownPeer.IPAddr,
			DomainName:   knownPeer.DomainName,
			Connected:    h.p2p.IsConnected(id),
			Confirmed:    knownPeer.Confirmed,
			Declined:     knownPeer.Declined,
			LastSeen:     knownPeer.LastSeen,
			Connections:  h.p2p.PeerConnectionsInfo(id),
			NetworkStats: h.p2p.NetworkStatsForPeer(id),
		}
		result = append(result, kpr)
	}

	// list connected peers first
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Connected && !result[j].Connected
	})

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
// @Router /peers/get_known_peer_settings [POST]
func (h *Handler) GetKnownPeerSettings(c echo.Context) (err error) {
	req := entity.PeerIDRequest{}
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

	return c.JSON(http.StatusOK, knownPeer)
}

// @Tags Peers
// @Summary Update peer settings
// @Accept json
// @Produce json
// @Param body body entity.UpdatePeerSettingsRequest true "Params"
// @Success 200 "OK"
// @Failure 400 {object} api.Error
// @Failure 404 {object} api.Error
// @Router /peers/update_settings [POST]
func (h *Handler) UpdatePeerSettings(c echo.Context) (err error) {
	req := entity.UpdatePeerSettingsRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	} else if !awldns.IsValidDomainName(req.DomainName) {
		return c.JSON(http.StatusBadRequest, ErrorMessage("invalid domain name"))
	}

	knownPeer, exists := h.conf.GetPeer(req.PeerID)
	if !exists {
		return c.JSON(http.StatusNotFound, ErrorMessage("peer not found"))
	}
	peerID := knownPeer.PeerId()

	knownPeer.Alias = req.Alias
	knownPeer.DomainName = req.DomainName
	h.conf.UpsertPeer(knownPeer)

	// not necessary now because we do not send anything
	go func() {
		_ = h.authStatus.ExchangeNewStatusInfo(h.ctx, peerID, knownPeer)
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
		PeerID:    req.PeerID,
		Name:      "",
		Alias:     req.Alias,
		IPAddr:    ipAddr,
		Confirmed: false,
		CreatedAt: time.Now(),
	}
	newPeerConfig.DomainName = awldns.TrimDomainName(newPeerConfig.DisplayName())
	h.conf.RemoveDeclinedPeer(req.PeerID)
	h.conf.UpsertPeer(newPeerConfig)
	h.p2p.ProtectPeer(peerId)
	h.tunnel.RefreshPeersList()

	go func() {
		authPeer := protocol.AuthPeer{
			Name: h.conf.P2pNode.Name,
		}
		_ = h.authStatus.SendAuthRequest(h.ctx, peerId, authPeer)

		knownPeer, _ := h.conf.GetPeer(req.PeerID)
		_ = h.authStatus.ExchangeNewStatusInfo(h.ctx, peerId, knownPeer)
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
		PeerID:    req.PeerID,
		Name:      auth.Name,
		Alias:     req.Alias,
		IPAddr:    ipAddr,
		Confirmed: true,
		CreatedAt: time.Now(),
	}
	newPeerConfig.DomainName = awldns.TrimDomainName(newPeerConfig.DisplayName())
	h.conf.RemoveDeclinedPeer(req.PeerID)
	h.conf.UpsertPeer(newPeerConfig)
	h.p2p.ProtectPeer(peerId)
	h.tunnel.RefreshPeersList()

	go func() {
		_ = h.authStatus.ExchangeNewStatusInfo(h.ctx, peerId, newPeerConfig)
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

// @Tags Peers
// @Summary Remove known peer
// @Accept json
// @Produce json
// @Param body body entity.PeerIDRequest true "Params"
// @Success 200 "OK"
// @Failure 400 {object} api.Error
// @Failure 404 {object} api.Error
// @Router /peers/remove [POST]
func (h *Handler) RemovePeer(c echo.Context) (err error) {
	req := entity.PeerIDRequest{}
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

	knownPeer, exists := h.conf.RemovePeer(req.PeerID)
	if !exists {
		return c.JSON(http.StatusNotFound, ErrorMessage("peer not found"))
	}

	h.p2p.UnprotectPeer(peerId)
	h.tunnel.RefreshPeersList()
	h.authStatus.DeclinePeer(knownPeer)

	return c.NoContent(http.StatusOK)
}
