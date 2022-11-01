package api

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"unicode/utf8"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/labstack/echo/v4"
	"github.com/libp2p/go-libp2p/core/metrics"
	ma "github.com/multiformats/go-multiaddr"
	"go.uber.org/zap/zapcore"
)

// @Tags Debug
// @Summary Get p2p debug info
// @Produce json
// @Success 200 {object} entity.P2pDebugInfo
// @Router /debug/p2p_info [GET]
func (h *Handler) GetP2pDebugInfo(c echo.Context) (err error) {
	metricsByProtocol := h.p2p.NetworkStatsByProtocol()
	bandwidthByProtocol := make(map[string]entity.BandwidthInfo, len(metricsByProtocol))
	for key, val := range metricsByProtocol {
		bandwidthByProtocol[string(key)] = makeBandwidthInfo(val)
	}

	debugInfo := entity.P2pDebugInfo{
		General: entity.GeneralDebugInfo{
			Version: config.Version,
			Uptime:  h.p2p.Uptime().String(),
		},
		DHT: entity.DhtDebugInfo{
			RoutingTableSize:    h.p2p.RoutingTableSize(),
			RoutingTable:        h.p2p.RoutingTablePeers(),
			Reachability:        h.p2p.Reachability().String(),
			ListenAddress:       maToStrings(h.p2p.AnnouncedAs()),
			PeersWithAddrsCount: h.p2p.PeersWithAddrsCount(),
			ObservedAddrs:       maToStrings(h.p2p.OwnObservedAddrs()),
			BootstrapPeers:      h.p2p.BootstrapPeersStatsDetailed(),
		},
		Connections: entity.ConnectionsDebugInfo{
			ConnectedPeersCount:  h.p2p.ConnectedPeersCount(),
			OpenConnectionsCount: h.p2p.OpenConnectionsCount(),
			OpenStreamsCount:     h.p2p.OpenStreamsCount(),
			LastTrimAgo:          h.p2p.ConnectionsLastTrimAgo().String(),
		},
		Bandwidth: entity.BandwidthDebugInfo{
			Total:      makeBandwidthInfo(h.p2p.NetworkStats()),
			ByProtocol: bandwidthByProtocol,
		},
	}

	return c.JSONPretty(http.StatusOK, debugInfo, "    ")
}

// @Tags Debug
// @Summary Get logs
// @Param logs query int false "Define number of rows of logs to output. On default and 0 prints all."
// @Param from_head query bool false "Print logs from the beginning of logs"
// @Produce plain
// @Success 200 {string} string "log text"
// @Router /debug/log [GET]
func (h *Handler) GetLog(c echo.Context) (err error) {
	req := entity.LogRequest{}
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}
	if err = c.Validate(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorMessage(err.Error()))
	}

	b := h.logBuffer.Bytes()
	if !utf8.Valid(b) {
		b = bytes.ToValidUTF8(b, []byte(""))
	}
	b = bytes.Trim(b, zapcore.DefaultLineEnding)

	logLines := bytes.Split(b, []byte(zapcore.DefaultLineEnding))
	if req.LogsRows == 0 || len(logLines) <= req.LogsRows {
		return c.Blob(http.StatusOK, echo.MIMETextPlainCharsetUTF8, b)
	}

	if req.StartFromHead {
		logLines = logLines[:req.LogsRows]
	} else {
		logLines = logLines[len(logLines)-req.LogsRows:]
	}
	b = bytes.Join(logLines, []byte(zapcore.DefaultLineEnding))
	return c.Blob(http.StatusOK, echo.MIMETextPlainCharsetUTF8, b)
}

func makeBandwidthInfo(stats metrics.Stats) entity.BandwidthInfo {
	return entity.BandwidthInfo{
		TotalIn:  byteCountIEC(stats.TotalIn),
		TotalOut: byteCountIEC(stats.TotalOut),
		RateIn:   byteCountIEC(int64(stats.RateIn)),
		RateOut:  byteCountIEC(int64(stats.RateOut)),
	}
}

func byteCountIEC(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB",
		float64(b)/float64(div), "KMGTPE"[exp])
}

func maToStrings(addrs []ma.Multiaddr) []string {
	res := make([]string, 0, len(addrs))
	for i := range addrs {
		res = append(res, addrs[i].String())
	}
	sort.Strings(res)

	return res
}
