package api

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
)

// @Tags		Settings
// @Summary	Get my peer info
// @Accept		json
// @Produce	json
// @Success	200	{object}	entity.PeerInfo
// @Router		/settings/peer_info [GET]
func (h *Handler) GetMyPeerInfo(c echo.Context) (err error) {
	totalBootstraps, connectedBootstraps := h.p2p.BootstrapPeersStats()
	netStats := h.p2p.NetworkStats()

	// Snapshot the runtime-mutable scalar config fields under the read lock.
	// P2pNode.Name is mutated by UpdateMySettings; VPNConfig fields may change
	// once runtime reconfiguration lands — read them under the lock too.
	h.conf.RLock()
	p2pNode := h.conf.P2pNode
	vpnConfig := h.conf.VPNConfig
	h.conf.RUnlock()

	peerInfo := entity.PeerInfo{
		PeerID:                  p2pNode.PeerID,
		Name:                    p2pNode.Name,
		Uptime:                  h.p2p.Uptime(),
		ServerVersion:           config.Version,
		NetworkStats:            netStats,
		NetworkStatsInIECUnits:  getStatsInIECUnits(netStats),
		TotalBootstrapPeers:     totalBootstraps,
		ConnectedBootstrapPeers: connectedBootstraps,
		Reachability:            h.p2p.Reachability().String(),
		AwlDNSAddress:           h.dns.AwlDNSAddress(),
		IsAwlDNSSetAsSystem:     h.dns.IsAwlDNSSetAsSystem(),
		VPN: entity.VPNInfo{
			VPNInterfaceEnabled: h.tunnel != nil,
			InterfaceName:       vpnConfig.InterfaceName,
			IPNet:               vpnConfig.IPNet,
		},
		SOCKS5: func() entity.SOCKS5Info {
			h.conf.RLock()
			socks5 := h.conf.SOCKS5
			h.conf.RUnlock()

			info := entity.SOCKS5Info{
				ListenAddress:   socks5.ListenAddress,
				ProxyingEnabled: socks5.ProxyingEnabled,
				ListenerEnabled: socks5.ListenerEnabled,
				UsingPeerID:     socks5.UsingPeerID,
			}

			if socks5.UsingPeerID != "" {
				if peer, ok := h.conf.GetPeer(socks5.UsingPeerID); ok {
					peerID := peer.PeerId()
					info.Connected = h.p2p.IsConnected(peerID)
					info.UsingPeerName = peer.DisplayName()
					info.UsingPeerPublicIP = h.p2p.PeerPublicIP(peerID)
					if info.Connected {
						info.UsingPeerPing = h.p2p.GetPeerLatency(peerID)
						info.UsingPeerThroughRelay = !h.p2p.HasDirectConnection(peerID)
					}
				}
			}

			return info
		}(),
		VPNGateway: func() entity.VPNGatewayInfo {
			h.conf.RLock()
			gw := h.conf.VPNGateway
			h.conf.RUnlock()
			info := entity.VPNGatewayInfo{
				ClientEnabled: gw.ClientEnabled,
				GatewayPeerID: gw.GatewayPeerID,
				ServerEnabled: gw.ServerEnabled,
			}
			if gw.GatewayPeerID != "" {
				if peer, ok := h.conf.GetPeer(gw.GatewayPeerID); ok {
					peerID := peer.PeerId()
					info.GatewayPeerName = peer.DisplayName()
					info.Connected = h.p2p.IsConnected(peerID)
					info.GatewayPublicIP = h.p2p.PeerPublicIP(peerID)
					if info.Connected {
						info.GatewayPing = h.p2p.GetPeerLatency(peerID)
						info.GatewayThroughRelay = !h.p2p.HasDirectConnection(peerID)
					}
				}
			}
			return info
		}(),
	}

	return c.JSON(http.StatusOK, peerInfo)
}

// @Tags		Settings
// @Summary	Update my peer info
// @Accept		json
// @Produce	json
// @Param		body	body	entity.UpdateMySettingsRequest	true	"Params"
// @Success	200		"OK"
// @Router		/settings/update [POST]
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
	h.conf.Save()

	go func() {
		h.authStatus.ExchangeStatusInfoWithAllKnownPeers(h.ctx)
	}()

	return c.NoContent(http.StatusOK)
}

// @Tags		Settings
// @Summary	Export server configuration
// @Accept		json
// @Produce	json
// @Success	200	{object}	config.Config
// @Router		/settings/export_server_config [GET]
func (h *Handler) ExportServerConfiguration(c echo.Context) (err error) {
	data := h.conf.Export()

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSON, data)
}

// @Tags		Settings
// @Summary	List available socks5 proxies
// @Accept		json
// @Produce	json
// @Success	200	{object}	entity.ListAvailableProxiesResponse
// @Router		/settings/list_proxies [GET]
func (h *Handler) ListAvailableProxies(c echo.Context) (err error) {
	proxies := h.socks5.ListAvailableProxies()

	response := entity.ListAvailableProxiesResponse{
		Proxies: proxies,
	}

	return c.JSON(http.StatusOK, response)
}

// @Tags		Settings
// @Summary	Update current proxy settings
// @Accept		json
// @Produce	json
// @Param		body	body	entity.UpdateProxySettingsRequest	true	"Params"
// @Success	200		"OK"
// @Router		/settings/set_proxy [POST]
func (h *Handler) UpdateProxySettings(c echo.Context) (err error) {
	req := entity.UpdateProxySettingsRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	peer, ok := h.conf.GetPeer(req.UsingPeerID)
	if req.UsingPeerID == "" {
		// ok
	} else if !ok {
		return c.JSON(http.StatusNotFound, ErrorMessage("peer not found"))
	} else if !peer.AllowedUsingAsExitNode {
		return c.JSON(http.StatusBadRequest, ErrorMessage("peer doesn't allow using as exit node"))
	}

	h.socks5.SetProxyPeerID(req.UsingPeerID)

	return c.NoContent(http.StatusOK)
}
