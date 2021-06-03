package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p-core/protocol"
)

const (
	Version = "0.2.0"

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

func ReadUint64(stream io.Reader) (uint64, error) {
	var data [8]byte
	n, err := stream.Read(data[:])
	if err != nil {
		return 0, err
	}
	if n != 8 {
		return 0, fmt.Errorf("invalid uint64 data: %v. read %d instead of 8", data, n)
	}

	value := binary.BigEndian.Uint64(data[:])
	return value, nil
}

func WriteUint64(stream io.Writer, number uint64) error {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], number)
	_, err := stream.Write(data[:])
	return err
}
