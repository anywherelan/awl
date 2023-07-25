package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/labstack/echo/v4"
	"github.com/libp2p/go-libp2p/core/peer"
)

const ErrorPeerAliasIsNotUniq = "peer name is not unique"

// @Tags Peers
// @Summary Get known peers info
// @Accept json
// @Produce json
// @Success 200 {array} entity.KnownPeersResponse
// @Router /peers/get_known [GET]
func (h *Handler) GetKnownPeers(c echo.Context) (err error) {
	h.conf.RLock()
	result := make([]entity.KnownPeersResponse, 0, len(h.conf.KnownPeers))
	peers := make([]string, 0, len(h.conf.KnownPeers))
	for peerID := range h.conf.KnownPeers {
		peers = append(peers, peerID)
	}
	h.conf.RUnlock()
	sort.Strings(peers)

	for _, peerID := range peers {
		knownPeer, _ := h.conf.GetPeer(peerID)

		id := knownPeer.PeerId()
		netStats := h.p2p.NetworkStatsForPeer(id)
		kpr := entity.KnownPeersResponse{
			PeerID:                 peerID,
			Name:                   knownPeer.DisplayName(),
			DisplayName:            knownPeer.DisplayName(),
			Alias:                  knownPeer.Alias,
			Version:                config.VersionFromUserAgent(h.p2p.PeerUserAgent(id)),
			IpAddr:                 knownPeer.IPAddr,
			DomainName:             knownPeer.DomainName,
			Connected:              h.p2p.IsConnected(id),
			Confirmed:              knownPeer.Confirmed,
			Declined:               knownPeer.Declined,
			WeAllowUsingAsExitNode: knownPeer.WeAllowUsingAsExitNode,
			AllowedUsingAsExitNode: knownPeer.AllowedUsingAsExitNode,
			LastSeen:               knownPeer.LastSeen,
			Connections:            h.p2p.PeerConnectionsInfo(id),
			NetworkStats:           netStats,
			NetworkStatsInIECUnits: getStatsInIECUnits(netStats),
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

	req.Alias = strings.TrimSpace(req.Alias)
	if !h.conf.IsUniqPeerAlias(req.PeerID, req.Alias) {
		return c.JSON(http.StatusBadRequest, ErrorMessage(ErrorPeerAliasIsNotUniq))
	}
	knownPeer.Alias = req.Alias
	knownPeer.DomainName = req.DomainName
	knownPeer.WeAllowUsingAsExitNode = req.AllowUsingAsExitNode

	h.conf.UpsertPeer(knownPeer)

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

	req.Alias = strings.TrimSpace(req.Alias)
	if !h.conf.IsUniqPeerAlias("", req.Alias) {
		return c.JSON(http.StatusBadRequest, ErrorMessage(ErrorPeerAliasIsNotUniq))
	}

	h.authStatus.AddPeer(h.ctx, peerId, "", req.Alias, false)

	return c.NoContent(http.StatusOK)
}

// @Tags Peers
// @Summary Accept new peer's invitation
// @Accept json
// @Produce json
// @Param body body entity.FriendRequestReply true "Params"
// @Success 200 "OK"
// @Failure 400 {object} api.Error
// @Failure 500 {object} api.Error
// @Router /peers/accept_peer [POST]
func (h *Handler) AcceptFriend(c echo.Context) (err error) {
	req := entity.FriendRequestReply{}
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

	if req.Decline {
		h.authStatus.BlockPeer(peerId, auth.Name)
		return c.NoContent(http.StatusOK)
	}

	req.Alias = strings.TrimSpace(req.Alias)
	if !h.conf.IsUniqPeerAlias("", req.Alias) {
		return c.JSON(http.StatusBadRequest, ErrorMessage(ErrorPeerAliasIsNotUniq))
	}

	h.authStatus.AddPeer(h.ctx, peerId, auth.Name, req.Alias, true)

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
	h.authStatus.BlockPeer(peerId, knownPeer.DisplayName())

	return c.NoContent(http.StatusOK)
}

// @Tags Peers
// @Summary Get blocked peers info
// @Accept json
// @Produce json
// @Success 200 {array} config.BlockedPeer
// @Router /peers/get_blocked [GET]
func (h *Handler) GetBlockedPeers(c echo.Context) (err error) {
	h.conf.RLock()
	result := make([]config.BlockedPeer, 0)

	for _, blockedPeer := range h.conf.BlockedPeers {
		result = append(result, blockedPeer)
	}

	sort.SliceStable(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	h.conf.RUnlock()

	return c.JSON(http.StatusOK, result)
}
