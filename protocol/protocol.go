package protocol

import (
	"encoding/json"
	"io"

	"github.com/libp2p/go-libp2p-core/protocol"
)

const (
	Version = "0.1.0"

	AuthMethod         protocol.ID = "/awl/" + Version + "/auth/"
	GetStatusMethod    protocol.ID = "/awl/" + Version + "/status/"
	TunnelPacketMethod protocol.ID = "/awl/" + Version + "/tunnel/"
)

type (
	PeerStatusInfo struct {
		Name string
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
}

type AuthPeerResponse struct {
	Confirmed bool
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
