package metrics

// Read from the node on every scrape, see CollectFrom.
var (
	nodeInfoDesc = newDesc("node", "info",
		"Static node information.",
		"version", "peer_id", "go_version", "os", "arch", "app")

	nodeUptimeSecondsDesc = newDesc("node", "uptime_seconds",
		"Node uptime in seconds.")

	nodeStartTimestampDesc = newDesc("node", "start_timestamp",
		"Unix timestamp of node start.")
)
