package protocol

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestAppendPacketToBuf(t *testing.T) {
	packet := []byte{1, 2, 3, 4, 5}
	buf := AppendPacketToBuf(nil, packet)

	require.Len(t, buf, 8+len(packet))

	length, err := ReadUint64(bytes.NewReader(buf))
	require.NoError(t, err)
	require.Equal(t, uint64(len(packet)), length)
	require.Equal(t, packet, buf[8:])
}

func TestAppendPacketToBuf_EmptyPacket(t *testing.T) {
	buf := AppendPacketToBuf(nil, []byte{})
	require.Len(t, buf, 8)

	length, err := ReadUint64(bytes.NewReader(buf))
	require.NoError(t, err)
	require.Equal(t, uint64(0), length)
}
