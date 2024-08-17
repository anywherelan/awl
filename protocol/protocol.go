package protocol

import (
	"encoding/binary"
	"encoding/json"
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
)

type (
	PeerStatusInfo struct {
		Name                 string
		Declined             bool
		AllowUsingAsExitNode bool
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

func ReadUint64(stream io.Reader) (uint64, error) {
	var data [8]byte
	n, err := io.ReadFull(stream, data[:])
	if err != nil {
		return 0, err
	}
	if n != 8 {
		return 0, fmt.Errorf("invalid uint64 data: %v. read %d instead of 8", data, n)
	}

	value := binary.BigEndian.Uint64(data[:])
	return value, nil
}

func WritePacketToBuf(buf, packet []byte) []byte {
	const lenBytesCount = 8
	binary.BigEndian.PutUint64(buf, uint64(len(packet)))
	n := copy(buf[lenBytesCount:], packet)

	return buf[:lenBytesCount+n]
}
