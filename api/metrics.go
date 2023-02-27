package api

import (
	"fmt"
	"math"
	"strconv"

	"github.com/anywherelan/awl/entity"
	"github.com/libp2p/go-libp2p/core/metrics"
)

func getStatsInIECUnits(stats metrics.Stats) entity.StatsInUnits {
	return entity.StatsInUnits{
		TotalIn:  convertBytesToIECUnits(float64(stats.TotalIn)),
		TotalOut: convertBytesToIECUnits(float64(stats.TotalOut)),
		RateIn:   convertBytesToIECUnits(stats.RateIn) + "/s",
		RateOut:  convertBytesToIECUnits(stats.RateOut) + "/s",
	}
}

func convertBytesToIECUnits(bytesSize float64) string {
	const unit = float64(1024)
	IECUnits := [9]string{
		"",
		"Ki",
		"Mi",
		"Gi",
		"Ti",
		"Pi",
		"Ei",
		"Zi",
		"Yi",
	}

	label := 0
	for label < len(IECUnits) && bytesSize >= unit {
		bytesSize /= unit
		label++
	}

	bytesSize = math.Round(bytesSize*100) / 100
	bFormatted := strconv.FormatFloat(bytesSize, 'f', -1, 64)

	return fmt.Sprintf("%s %sB", bFormatted, IECUnits[label])
}
