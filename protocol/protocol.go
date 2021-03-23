package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p-core/protocol"
)

const (
	Version = "0.1.0"

	PortForwardingMethod protocol.ID = "/awl/" + Version + "/forward/"
	AuthMethod           protocol.ID = "/awl/" + Version + "/auth/"
	GetStatusMethod      protocol.ID = "/awl/" + Version + "/status/"
)

func HandleForwardPortStream(stream io.Reader) (int, error) {
	data := make([]byte, 8)
	n, err := stream.Read(data)
	if err != nil {
		return 0, fmt.Errorf("unable to read first packet of stream: %v", err)
	}
	// если первый пакет не N байт - значит что-то не так, протокол не соблюден, можно дропать
	if n != 8 {
		return 0, fmt.Errorf("invalid command data: %v. len %d instead of 8", data, len(data))
	}

	port := binary.BigEndian.Uint64(data)
	if port == 0 {
		return 0, errors.New("invalid data. port could not be 0")
	}
	return int(port), nil
}

func PackForwardPortData(port int) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, uint64(port))
	return data
}

type (
	PeerStatusInfo struct {
		Name           string
		PermittedPorts []PermittedPort
	}
	PermittedPort struct {
		Port        int
		Description string
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
