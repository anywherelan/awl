package api

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/peerlan/peerlan/entity"
)

// @Tags Connections
// @Summary Get inbound connections
// @Accept json
// @Produce json
// @Success 200 {object} entity.GetInboundConnectionsResponse
// @Router /connections/inbound [GET]
func (h *Handler) GetInboundConnections(c echo.Context) (err error) {
	inboundStreams := h.forwarding.GetAllInboundStreams()
	result := make(map[int][]entity.InboundStream)
	duplicateMap := make(map[string]struct{})

	for _, stream := range inboundStreams {
		key := strconv.Itoa(stream.LocalPort) + stream.PeerID
		if _, exists := duplicateMap[key]; exists {
			continue
		}

		duplicateMap[key] = struct{}{}
		result[stream.LocalPort] = append(result[stream.LocalPort], stream)
	}
	for port := range result {
		connections := result[port]
		sort.Slice(connections, func(i, j int) bool {
			return connections[i].PeerID < connections[j].PeerID
		})
		result[port] = connections
	}

	return c.JSON(http.StatusOK, result)
}

// @Tags Connections
// @Summary Get outbound connections
// @Accept json
// @Produce json
// @Success 200 {array} entity.ForwardedPort
// @Router /connections/forwarded_ports [GET]
func (h *Handler) GetForwardedPorts(c echo.Context) (err error) {
	forwardedPorts := h.forwarding.GetForwardedPorts()
	sort.Slice(forwardedPorts, func(i, j int) bool {
		return forwardedPorts[i].ListenAddress < forwardedPorts[j].ListenAddress
	})

	return c.JSON(http.StatusOK, forwardedPorts)
}
