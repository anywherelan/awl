package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"runtime"
	"strings"
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

// TestReceiveRejectsOversizedMessages: a message over its limit fails with
// ErrMessageTooLarge, and one that fits still decodes.
func TestReceiveRejectsOversizedMessages(t *testing.T) {
	encode := func(t *testing.T, send func(*bytes.Buffer) error) *bytes.Buffer {
		t.Helper()
		var buf bytes.Buffer
		require.NoError(t, send(&buf))
		return &buf
	}
	// The padding takes the whole limit minus the JSON around it, measured on a
	// message padded with one byte. It goes in a field that is not normalized,
	// unlike the name. The trailing newline of the encoder is not part of the
	// value and is never read.
	padOf := func(limit int, onePadded *bytes.Buffer) string {
		return strings.Repeat("a", limit-(onePadded.Len()-len("a\n")))
	}

	t.Run("auth", func(t *testing.T) {
		onePadded := encode(t, func(b *bytes.Buffer) error { return SendAuth(b, AuthPeer{Token: "a"}) })
		fits := AuthPeer{Token: padOf(MaxAuthMessageSize, onePadded)}
		got, err := ReceiveAuth(encode(t, func(b *bytes.Buffer) error { return SendAuth(b, fits) }))
		require.NoError(t, err)
		require.Equal(t, fits, got)

		tooLarge := AuthPeer{Token: fits.Token + "a"}
		_, err = ReceiveAuth(encode(t, func(b *bytes.Buffer) error { return SendAuth(b, tooLarge) }))
		require.ErrorIs(t, err, ErrMessageTooLarge)
	})

	t.Run("status", func(t *testing.T) {
		onePadded := encode(t, func(b *bytes.Buffer) error { return SendStatus(b, PeerStatusInfo{IPv6Addr: "a"}) })
		fits := PeerStatusInfo{IPv6Addr: padOf(MaxStatusMessageSize, onePadded)}
		got, err := ReceiveStatus(encode(t, func(b *bytes.Buffer) error { return SendStatus(b, fits) }))
		require.NoError(t, err)
		require.Equal(t, fits, got)

		tooLarge := PeerStatusInfo{IPv6Addr: fits.IPv6Addr + "a"}
		_, err = ReceiveStatus(encode(t, func(b *bytes.Buffer) error { return SendStatus(b, tooLarge) }))
		require.ErrorIs(t, err, ErrMessageTooLarge)
	})

	t.Run("auth response", func(t *testing.T) {
		padded := `{"Confirmed":true,"Padding":"` + strings.Repeat("a", MaxAuthMessageSize) + `"}`
		_, err := ReceiveAuthResponse(strings.NewReader(padded))
		require.ErrorIs(t, err, ErrMessageTooLarge)
	})
}

// TestReceiveAuthBoundsMemory is the advisory's attack: an auth request with a
// never-ending name. Before the limit, a 256 MiB name cost 1 GiB of allocations.
func TestReceiveAuthBoundsMemory(t *testing.T) {
	endless := io.MultiReader(strings.NewReader(`{"Name":"`), repeatReader('A'))

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ReceiveAuth(endless)
	runtime.ReadMemStats(&after)

	require.ErrorIs(t, err, ErrMessageTooLarge)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4*MaxAuthMessageSize+1<<20))
}

// repeatReader is an endless stream of one byte.
type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
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
	binary.BigEndian.PutUint64(header[:], uint64(vpn.MaxPacketBodySize)+1)

	_, _, err := ReadPacketHeader(bytes.NewReader(header[:]))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds max")
}

func TestReadPacketHeader_AcceptsMaxBodySize(t *testing.T) {
	var header [8]byte
	binary.BigEndian.PutUint64(header[:], uint64(vpn.MaxPacketBodySize))

	size, _, err := ReadPacketHeader(bytes.NewReader(header[:]))
	require.NoError(t, err)
	require.Equal(t, uint64(vpn.MaxPacketBodySize), size)
}

func TestReadPacketHeader_TruncatedStream(t *testing.T) {
	_, _, err := ReadPacketHeader(bytes.NewReader([]byte{1, 2, 3}))
	require.Error(t, err)
}
