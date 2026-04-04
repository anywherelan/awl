package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anywherelan/awl/vpn"
)

func TestSendReceiveStatus(t *testing.T) {
	cases := []PeerStatusInfo{
		{Name: "alice", Declined: false, AllowUsingAsExitNode: false},
		{Name: "bob", Declined: true, AllowUsingAsExitNode: true},
		{Name: "", Declined: false, AllowUsingAsExitNode: false},
	}

	for _, want := range cases {
		var buf bytes.Buffer
		require.NoError(t, SendStatus(&buf, want))
		got, err := ReceiveStatus(&buf)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestSendReceiveAuth(t *testing.T) {
	cases := []AuthPeer{
		{Name: "alice"},
		{Name: ""},
	}

	for _, want := range cases {
		var buf bytes.Buffer
		require.NoError(t, SendAuth(&buf, want))
		got, err := ReceiveAuth(&buf)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestSendReceiveAuthResponse(t *testing.T) {
	cases := []AuthPeerResponse{
		{Confirmed: true, Declined: false},
		{Confirmed: false, Declined: true},
		{Confirmed: false, Declined: false},
	}

	for _, want := range cases {
		var buf bytes.Buffer
		require.NoError(t, SendAuthResponse(&buf, want))
		got, err := ReceiveAuthResponse(&buf)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestAppendPacketToBuf_RoundTrip(t *testing.T) {
	packet := []byte{1, 2, 3, 4, 5}
	cases := []vpn.GatewayDir{vpn.GatewayDirNone, vpn.GatewayDirForward, vpn.GatewayDirReturn}

	for _, dir := range cases {
		buf := AppendPacketToBuf(nil, packet, dir)
		require.Len(t, buf, 8+len(packet))

		size, gotDir, err := ReadPacketHeader(bytes.NewReader(buf))
		require.NoError(t, err)
		require.Equal(t, uint64(len(packet)), size)
		require.Equal(t, dir, gotDir)
		require.Equal(t, packet, buf[8:])
	}
}

func TestAppendPacketToBuf_EmptyPacket(t *testing.T) {
	buf := AppendPacketToBuf(nil, []byte{}, vpn.GatewayDirNone)
	require.Len(t, buf, 8)

	size, dir, err := ReadPacketHeader(bytes.NewReader(buf))
	require.NoError(t, err)
	require.Equal(t, uint64(0), size)
	require.Equal(t, vpn.GatewayDirNone, dir)
}

func TestReadPacketHeader_RejectsBothFlags(t *testing.T) {
	var header [8]byte
	v := uint64(100) | tunnelFlagGatewayForward | tunnelFlagGatewayReturn
	binary.BigEndian.PutUint64(header[:], v)

	_, _, err := ReadPacketHeader(bytes.NewReader(header[:]))
	require.Error(t, err)
	require.Contains(t, err.Error(), "both gateway flags")
}

func TestReadPacketHeader_RejectsHugeSize(t *testing.T) {
	var header [8]byte
	binary.BigEndian.PutUint64(header[:], tunnelMaxLength+1)

	_, _, err := ReadPacketHeader(bytes.NewReader(header[:]))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds max")
}

func TestReadPacketHeader_TruncatedStream(t *testing.T) {
	_, _, err := ReadPacketHeader(bytes.NewReader([]byte{1, 2, 3}))
	require.Error(t, err)
}
