package protocol

import (
	"encoding/json"
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
		IPv6Addr                string `json:"ipv6Addr,omitempty"`
	}
)

func ReceiveStatus(stream io.Reader) (PeerStatusInfo, error) {
	statusInfo := PeerStatusInfo{}
	err := json.NewDecoder(stream).Decode(&statusInfo)
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
}

type AuthPeerResponse struct {
	Confirmed bool
	Declined  bool
}

func ReceiveAuth(stream io.Reader) (AuthPeer, error) {
	authPeer := AuthPeer{}
	err := json.NewDecoder(stream).Decode(&authPeer)
	return authPeer, err
}

func SendAuth(stream io.Writer, authPeer AuthPeer) error {
	err := json.NewEncoder(stream).Encode(&authPeer)
	return err
}

func ReceiveAuthResponse(stream io.Reader) (AuthPeerResponse, error) {
	response := AuthPeerResponse{}
	err := json.NewDecoder(stream).Decode(&response)
	return response, err
}

func SendAuthResponse(stream io.Writer, response AuthPeerResponse) error {
	err := json.NewEncoder(stream).Encode(&response)
	return err
}
