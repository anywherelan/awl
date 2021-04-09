package vpn

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// TODO: also test tcp packets, ip packets with variable header size
func TestPacket_RecalculateChecksum(t *testing.T) {
	a := require.New(t)
	packet, rawData := testUDPPacket()
	packet.RecalculateChecksum()
	a.Equal(rawData, packet.Packet)
}

// TODO: bench with bigger packet
func BenchmarkPacket_RecalculateChecksum(b *testing.B) {
	packet, _ := testUDPPacket()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		packet.RecalculateChecksum()
	}
}

func testUDPPacket() (*Packet, []byte) {
	data, err := hex.DecodeString("4500002828f540004011fd490a4200010a420002a9d0238200148bfd68656c6c6f20776f726c6421")
	if err != nil {
		panic(err)
	}

	packet := new(Packet)
	_, _ = packet.ReadFrom(bytes.NewReader(data))
	packet.Parse()

	return packet, data
}
