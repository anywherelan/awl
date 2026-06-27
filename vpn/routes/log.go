package routes

import "github.com/ipfs/go-log/v2"

// logger is the package-level logger for the gateway routes/NAT setup. Only
// used to surface stale-state recovery and other one-off events that callers
// shouldn't have to thread through return values.
var logger = log.Logger("awl/vpn/routes")
