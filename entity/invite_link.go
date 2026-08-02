package entity

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Invite link format: awl://invite?p=<peer_id>&t=<token>&n=<name>
//
// The peer ID lives in the query, not in the authority, on purpose: it is
// base58 and therefore case-sensitive, while hosts are historically
// case-insensitive and get lowercased by messenger linkifiers and Android
// intent matching, which would break peer.Decode. The authority is the fixed
// word "invite", leaving room for other awl:// URLs later.
//
// The token is optional. With it the link is a capability — whoever presents it
// is added automatically; without it the link is just "here is me, with a name
// to fill in", the shareable form of a peer ID. One format covers both so that
// every consumer — the add form, the QR scanner, the CLI — has a single input
// to parse.
//
// The same format has a second implementation in Dart (awl-flutter), so the
// format is a contract and both sides carry tests for it.
const (
	InviteLinkScheme = "awl"
	InviteLinkHost   = "invite"
	// InviteLinkVersion is the newest format we understand. It is never written
	// into a link: adding a query parameter is backwards compatible on its own
	// (parsers ignore what they do not know), so a version only has to appear
	// the day something incompatible changes. Reading it is kept from day one,
	// so that such a link can be rejected with a comprehensible message instead
	// of a random parse failure.
	InviteLinkVersion = 1
)

// ErrNotInviteLink reports input that is not an awl invite link at all, as
// opposed to a malformed one. It lets a UI tell "this is a peer ID" from "this
// link is broken".
var ErrNotInviteLink = errors.New("not an awl invite link")

// InviteLink is the payload of an invite link: who invites, the bearer token
// granting automatic acceptance, and how the inviter calls itself.
type InviteLink struct {
	PeerID string
	// Token grants automatic acceptance to whoever presents it. Empty for a
	// link that only identifies its author, which is then added by hand as usual.
	Token string
	// Name is the creator's display name, used to prefill the add form so the
	// peer can be added before the creator ever comes online. Optional.
	Name string
}

// BuildInviteLink renders a link; an empty token or name is left out. The query
// is assembled by hand rather than through url.Values, which sorts its keys:
// parameters stay in the p, t, n order the format is written in, so links read
// the same way everywhere they are shown.
func BuildInviteLink(peerID, token, name string) string {
	params := []string{"p=" + url.QueryEscape(peerID)}
	if token != "" {
		params = append(params, "t="+url.QueryEscape(token))
	}
	if name != "" {
		params = append(params, "n="+url.QueryEscape(name))
	}

	link := url.URL{
		Scheme:   InviteLinkScheme,
		Host:     InviteLinkHost,
		RawQuery: strings.Join(params, "&"),
	}
	return link.String()
}

// ParseInviteLink parses a link, tolerating surrounding whitespace and newlines
// (links get copy-pasted out of messengers).
func ParseInviteLink(rawLink string) (InviteLink, error) {
	rawLink = strings.TrimSpace(rawLink)
	if rawLink == "" {
		return InviteLink{}, ErrNotInviteLink
	}

	parsed, err := url.Parse(rawLink)
	if err != nil {
		return InviteLink{}, fmt.Errorf("%w: %v", ErrNotInviteLink, err)
	}
	if !strings.EqualFold(parsed.Scheme, InviteLinkScheme) {
		return InviteLink{}, ErrNotInviteLink
	}
	if !strings.EqualFold(parsed.Host, InviteLinkHost) {
		return InviteLink{}, fmt.Errorf("unknown awl link type '%s'", parsed.Host)
	}

	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return InviteLink{}, fmt.Errorf("invalid invite link parameters: %v", err)
	}

	// A missing version means the original format, v1 — which is what every
	// link we produce is, since we never write the parameter.
	if rawVersion := query.Get("v"); rawVersion != "" {
		version, err := strconv.Atoi(rawVersion)
		if err != nil {
			return InviteLink{}, fmt.Errorf("invalid invite link version '%s'", rawVersion)
		}
		if version > InviteLinkVersion {
			return InviteLink{}, fmt.Errorf("this invite link was created by a newer version of anywherelan (link version %d, supported %d)",
				version, InviteLinkVersion)
		}
	}

	// Kept exactly as written: base58 peer IDs are case-sensitive.
	peerID := query.Get("p")
	if peerID == "" {
		return InviteLink{}, errors.New("invite link has no peer id")
	}
	if _, err := peer.Decode(peerID); err != nil {
		return InviteLink{}, fmt.Errorf("invite link has an invalid peer id: %v", err)
	}

	return InviteLink{
		PeerID: peerID,
		// Optional: without it the link cannot be redeemed, only added by hand.
		Token: query.Get("t"),
		Name:  query.Get("n"),
	}, nil
}
