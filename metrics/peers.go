package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// values of the path label of awl_peers_connected
const (
	pathDirect = "direct"
	pathRelay  = "relay"
)

// Read from the node on every scrape, see CollectFrom.
var (
	peersKnownTotalDesc = newDesc("peers", "known_total",
		"Total number of known peers.")

	peersConnectedDesc = newDesc("peers", "connected",
		"Number of currently connected known peers, by path: direct, or only through a relay (relay).",
		"path")

	peersConfirmedTotalDesc = newDesc("peers", "confirmed_total",
		"Number of confirmed (mutual) peers.")

	peersBlockedTotalDesc = newDesc("peers", "blocked_total",
		"Number of blocked peers.")

	peersAuthRequestsIngoingDesc = newDesc("peers", "auth_requests_ingoing",
		"Number of pending ingoing auth requests.")

	peersAuthRequestsOutgoingDesc = newDesc("peers", "auth_requests_outgoing",
		"Number of pending outgoing auth requests.")
)

var (
	PeersAuthRequestsSentTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "auth_requests_sent_total",
		Help:      "Total number of auth requests sent.",
	})

	PeersAuthRequestsReceivedTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "auth_requests_received_total",
		Help:      "Total number of auth requests received.",
	})

	PeersStatusRequestsSentTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "status_requests_sent_total",
		Help:      "Total number of status requests sent.",
	})

	PeersStatusRequestsReceivedTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "status_requests_received_total",
		Help:      "Total number of status requests received.",
	})

	InvitesRedeemedTotal = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "invites_redeemed_total",
		Help:      "Total number of invite links successfully redeemed by peers.",
	})

	// InviteTokensRejectedTotal explains why a peer that presented an invite
	// token still ended up in the manual accept queue.
	InviteTokensRejectedTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "invite_tokens_rejected_total",
		Help:      "Total number of invite tokens rejected, by reason.",
	}, []string{"reason"})

	PeersConnectionEventsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "peers",
		Name:      "connection_events_total",
		Help:      "Total peer connection events.",
	}, []string{"event"})
)
