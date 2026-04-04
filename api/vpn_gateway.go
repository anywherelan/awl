package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/anywherelan/awl/entity"
)

// EnableVPNGatewayClient turns on VPN gateway client mode.
//
// @Tags VPN Gateway
// @Summary Enable VPN gateway client mode
// @Accept json
// @Produce json
// @Param body body entity.EnableVPNGatewayClientRequest true "Params"
// @Success	200		"OK"
// @Router /vpn_gateway/client/enable [POST]
func (h *Handler) EnableVPNGatewayClient(c echo.Context) error {
	req := entity.EnableVPNGatewayClientRequest{}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err := c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	gatewayPeerID, err := peer.Decode(req.GatewayPeerID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage("invalid peer id: "+err.Error()))
	}

	if err := h.vpnGateway.EnableClient(gatewayPeerID); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	return c.NoContent(http.StatusOK)
}

// DisableVPNGatewayClient turns off VPN gateway client mode and persists the choice.
//
// @Tags VPN Gateway
// @Summary Disable VPN gateway client mode
// @Accept json
// @Produce json
// @Success	200		"OK"
// @Router /vpn_gateway/client/disable [POST]
func (h *Handler) DisableVPNGatewayClient(c echo.Context) error {
	h.vpnGateway.DisableClient()
	return c.NoContent(http.StatusOK)
}

// SetVPNGatewayServerEnabled toggles whether this node serves as a VPN
// gateway for permitted peers. Persisted; the new value propagates to other
// peers via the next status exchange (sub-minute). NAT / iptables state is
// (re)applied at runtime — no restart required.
//
// @Tags VPN Gateway
// @Summary Toggle VPN gateway server mode
// @Accept json
// @Produce json
// @Param body body entity.SetVPNGatewayServerEnabledRequest true "Params"
// @Success	200		"OK"
// @Router /vpn_gateway/server/set_enabled [POST]
func (h *Handler) SetVPNGatewayServerEnabled(c echo.Context) error {
	req := entity.SetVPNGatewayServerEnabledRequest{}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	if err := h.vpnGateway.SetServerEnabled(req.Enabled); err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorMessage(err.Error()))
	}

	go func() {
		h.authStatus.ExchangeStatusInfoWithAllKnownPeers(h.ctx)
	}()

	return c.NoContent(http.StatusOK)
}

// ListAvailableVPNGateways returns peers that are currently a valid VPN
// gateway target — i.e. they allow being used as an exit node and have VPN
// gateway server enabled on their side. Peers that allow exit-node use only
// for SOCKS5 are not included here.
//
// @Tags VPN Gateway
// @Summary List available VPN gateways
// @Accept json
// @Produce json
// @Success 200 {object} entity.ListAvailableVPNGatewaysResponse
// @Router /vpn_gateway/client/list_available [GET]
func (h *Handler) ListAvailableVPNGateways(c echo.Context) error {
	if h.vpnGateway == nil {
		return c.JSON(http.StatusOK, entity.ListAvailableVPNGatewaysResponse{})
	}
	return c.JSON(http.StatusOK, entity.ListAvailableVPNGatewaysResponse{
		VPNGateways: h.vpnGateway.ListAvailableVPNGateways(),
	})
}
