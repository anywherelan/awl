package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
)

const defaultInviteMaxUses = 1

// @Tags		Peers
// @Summary	Create an invite link
// @Accept		json
// @Produce	json
// @Param		body	body		entity.CreateInviteRequest	true	"Params"
// @Success	200		{object}	entity.InviteResponse
// @Failure	400		{object}	api.Error
// @Failure	500		{object}	api.Error
// @Router		/peers/invites/create [POST]
func (h *Handler) CreateInvite(c echo.Context) (err error) {
	req := entity.CreateInviteRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	if req.MaxUses == 0 {
		req.MaxUses = defaultInviteMaxUses
	}
	req.Alias = strings.TrimSpace(req.Alias)
	// One alias cannot name several peers, so it only makes sense for a
	// single-use link. Uniqueness itself is not checked here: the peer list will
	// have moved on by the time the link is redeemed, and a collision is
	// resolved then (GenUniqPeerAlias), so checking now would only produce
	// false rejections.
	if req.Alias != "" && req.MaxUses != 1 {
		return c.JSON(http.StatusBadRequest,
			ErrorMessage("alias can only be set for a single-use invite"))
	}

	var expiresAt time.Time
	if req.ExpiresInSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
	}

	invite, err := h.conf.CreateInvite(config.CreateInviteParams{
		Label:                req.Label,
		Alias:                req.Alias,
		AllowUsingAsExitNode: req.AllowUsingAsExitNode,
		MaxUses:              req.MaxUses,
		ExpiresAt:            expiresAt,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorMessage(err.Error()))
	}

	return c.JSON(http.StatusOK, h.inviteResponse(invite))
}

// @Tags		Peers
// @Summary	Get invite links
// @Accept		json
// @Produce	json
// @Success	200	{array}	entity.InviteResponse
// @Router		/peers/invites/list [GET]
func (h *Handler) GetInvites(c echo.Context) (err error) {
	invites := h.conf.ListInvites()

	result := make([]entity.InviteResponse, 0, len(invites))
	for _, invite := range invites {
		result = append(result, h.inviteResponse(invite))
	}

	return c.JSON(http.StatusOK, result)
}

// @Tags		Peers
// @Summary	Revoke an invite link
// @Description Stops new connections through the link; peers already added stay.
// @Accept		json
// @Produce	json
// @Param		body	body	entity.RevokeInviteRequest	true	"Params"
// @Success	200		"OK"
// @Failure	400		{object}	api.Error
// @Failure	404		{object}	api.Error
// @Router		/peers/invites/revoke [POST]
func (h *Handler) RevokeInvite(c echo.Context) (err error) {
	req := entity.RevokeInviteRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	if !h.conf.RevokeInvite(req.ID) {
		return c.JSON(http.StatusNotFound, ErrorMessage("invite not found"))
	}

	return c.NoContent(http.StatusOK)
}

// inviteResponse renders an invite for the API, building the link from our
// current peer ID and node name.
func (h *Handler) inviteResponse(invite config.Invite) entity.InviteResponse {
	h.conf.RLock()
	peerID := h.conf.P2pNode.PeerID
	nodeName := h.conf.P2pNode.Name
	h.conf.RUnlock()

	return entity.InviteResponse{
		ID:                   invite.ID,
		Label:                invite.Label,
		Link:                 entity.BuildInviteLink(peerID, invite.Token, nodeName),
		Alias:                invite.Alias,
		AllowUsingAsExitNode: invite.WeAllowUsingAsExitNode,
		MaxUses:              invite.MaxUses,
		UsedCount:            invite.UsedCount,
		ExpiresAt:            invite.ExpiresAt,
		CreatedAt:            invite.CreatedAt,
		Revoked:              invite.Revoked,
		Status:               inviteStatus(invite),
	}
}

func inviteStatus(invite config.Invite) string {
	switch {
	case invite.Revoked:
		return entity.InviteStatusRevoked
	case invite.IsExpired(time.Now()):
		return entity.InviteStatusExpired
	case invite.IsUsedUp():
		return entity.InviteStatusUsedUp
	}
	return entity.InviteStatusActive
}
