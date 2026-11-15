package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	version  = "0.3.0"
	basePath = "/awl/" + version

	AuthMethod         protocol.ID = basePath + "/auth/"
	GetStatusMethod    protocol.ID = basePath + "/status/"
	TunnelPacketMethod protocol.ID = basePath + "/tunnel/"
	Socks5PacketMethod protocol.ID = basePath + "/socks5/"
	Socks5NoAuthMethod protocol.ID = basePath + "/socks5-noauth/"
)

// Size limits for a single message, checked on receive. A message that
// outgrows its limit needs a new protocol: older peers would reject it.
const (
	// MaxAuthMessageSize limits AuthPeer and AuthPeerResponse. An auth request
	// comes from any node before we trust it, so the limit is kept small; the
	// messages are well under 1 KiB today.
	MaxAuthMessageSize = 64 << 10
	// MaxStatusMessageSize limits PeerStatusInfo, accepted from known peers only.
	MaxStatusMessageSize = 512 << 10
)

// ErrMessageTooLarge is returned when an incoming message exceeds its size limit.
var ErrMessageTooLarge = errors.New("message too large")

type (
	PeerStatusInfo struct {
		Name                 string
		Declined             bool
		AllowUsingAsExitNode bool
		// VPNGatewayServerEnabled mirrors VPNGatewayConfig.ServerEnabled on the
		// sender. The receiver stores it in KnownPeer.RemoteVPNGatewayServerEnabled
		// and uses KnownPeer.CanUseAsVPNGateway() to decide whether the
		// peer is a valid VPN gateway target.
		VPNGatewayServerEnabled bool
		// IPv4Addr is the sender's own awl IPv4 address. The receiver takes it
		// as its view of the sender if it is free in its subnet, see
		// config.KnownPeer.IPAddr. Empty from awl older than v0.21.0.
		IPv4Addr string `json:"ipv4Addr,omitempty"`
		IPv6Addr string `json:"ipv6Addr,omitempty"`
	}
)

func ReceiveStatus(stream io.Reader) (PeerStatusInfo, error) {
	statusInfo := PeerStatusInfo{}
	err := decodeLimited(stream, MaxStatusMessageSize, &statusInfo)
	statusInfo.Name = NormalizeName(statusInfo.Name)
	return statusInfo, err
}

func SendStatus(stream io.Writer, statusInfo PeerStatusInfo) error {
	err := json.NewEncoder(stream).Encode(&statusInfo)
	return err
}

type AuthPeer struct {
	Name string
	// Token is the secret from an invite link, presented by the peer that
	// redeems it. A valid token means the request is accepted automatically.
	//
	// Additive and backwards compatible: ReceiveAuth decodes without
	// DisallowUnknownFields, so an older awl ignores the field and falls back to
	// a manual accept, and a request from an older awl simply has none.
	//
	// The token must not leak outside this handshake: it travels over the
	// encrypted libp2p transport and AuthStreamHandler clears it before the
	// request is stored or emitted. entity.AuthRequest embeds this struct, so
	// the field is always empty there and swagger does not advertise it —
	// json:"-" is not an option, it would drop the token from the wire too.
	Token string `json:",omitempty" swaggerignore:"true"`
	// IPv4Addr is the sender's own awl IPv4 address, as in PeerStatusInfo. It
	// is only a suggestion for the accept form: the address is assigned on the
	// first status exchange. Additive like Token.
	IPv4Addr string `json:"ipv4Addr,omitempty"`
}

// AuthPeerResponse is the answer to an auth request: where the sender stands
// with us. Both false means the request is waiting for a manual accept.
type AuthPeerResponse struct {
	// Confirmed means the sender is in our KnownPeers. The sender takes it as a
	// confirmation and stops retrying.
	Confirmed bool
	// Declined means we have blocked the sender. It outranks Confirmed: a peer
	// can be in KnownPeers and BlockedPeers at once.
	Declined bool
}

func ReceiveAuth(stream io.Reader) (AuthPeer, error) {
	authPeer := AuthPeer{}
	err := decodeLimited(stream, MaxAuthMessageSize, &authPeer)
	authPeer.Name = NormalizeName(authPeer.Name)
	return authPeer, err
}

func SendAuth(stream io.Writer, authPeer AuthPeer) error {
	err := json.NewEncoder(stream).Encode(&authPeer)
	return err
}

func ReceiveAuthResponse(stream io.Reader) (AuthPeerResponse, error) {
	response := AuthPeerResponse{}
	err := decodeLimited(stream, MaxAuthMessageSize, &response)
	return response, err
}

func SendAuthResponse(stream io.Writer, response AuthPeerResponse) error {
	err := json.NewEncoder(stream).Encode(&response)
	return err
}

// decodeLimited decodes one JSON value from r, reading at most limit bytes.
// Without the limit the decoder buffers whatever the peer sends.
func decodeLimited(r io.Reader, limit int64, v any) error {
	lr := &io.LimitedReader{R: r, N: limit}
	err := json.NewDecoder(lr).Decode(v)
	// The limit used up and still no value: report that rather than the EOF
	// the decoder got at the limit
	if err != nil && lr.N <= 0 {
		return fmt.Errorf("%w: limit is %d bytes", ErrMessageTooLarge, limit)
	}
	return err
}
