//go:build !linux || android

package awl

import "github.com/ipfs/go-log/v2"

func appendSystemToolDirsToPath(*log.ZapEventLogger) {}
