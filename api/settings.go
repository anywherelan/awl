package api

import (
	"net/http"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/labstack/echo/v4"
)

// @Tags Settings
// @Summary Get my peer info
// @Accept json
// @Produce json
// @Success 200 {object} entity.PeerInfo
// @Router /settings/peer_info [GET].
func (h *Handler) GetMyPeerInfo(c echo.Context) (err error) {
	totalBootstraps, connectedBootstraps := h.p2p.BootstrapPeersStats()
	netStats := h.p2p.NetworkStats()

	peerInfo := entity.PeerInfo{
		PeerID:                  h.conf.P2pNode.PeerID,
		Name:                    h.conf.P2pNode.Name,
		Uptime:                  h.p2p.Uptime(),
		ServerVersion:           config.Version,
		NetworkStats:            netStats,
		NetworkStatsInIECUnits:  getStatsInIECUnits(netStats),
		TotalBootstrapPeers:     totalBootstraps,
		ConnectedBootstrapPeers: connectedBootstraps,
		Reachability:            h.p2p.Reachability().String(),
		AwlDNSAddress:           h.dns.AwlDNSAddress(),
		IsAwlDNSSetAsSystem:     h.dns.IsAwlDNSSetAsSystem(),
	}

	return c.JSON(http.StatusOK, peerInfo)
}

// @Tags Settings
// @Summary Update my peer info
// @Accept json
// @Produce json
// @Param body body entity.UpdateMySettingsRequest true "Params"
// @Success 200 "OK"
// @Router /settings/update [POST].
func (h *Handler) UpdateMySettings(c echo.Context) (err error) {
	req := entity.UpdateMySettingsRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	h.conf.Lock()
	h.conf.P2pNode.Name = req.Name
	h.conf.Unlock()

	go func() {
		h.authStatus.ExchangeStatusInfoWithAllKnownPeers(h.ctx)
	}()

	return c.NoContent(http.StatusOK)
}

// @Tags Settings
// @Summary Export server configuration
// @Accept json
// @Produce json
// @Success 200 {object} config.Config
// @Router /settings/export_server_config [GET].
func (h *Handler) ExportServerConfiguration(c echo.Context) (err error) {
	data := h.conf.Export()

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, data)
}
