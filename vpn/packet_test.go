package vpn

import (
	"bytes"
	"encoding/hex"
	"net"
	"reflect"
	"sync"
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

func BenchmarkPacket_PoolCopyToClear(b *testing.B) {
	packet, _ := testUDPPacket()

	packetsPool := sync.Pool{
		New: func() interface{} {
			return new(Packet)
		}}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		copyPacket := packetsPool.Get().(*Packet)
		packet.CopyTo(copyPacket)

		copyPacket.clear()
		packetsPool.Put(copyPacket)
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

func TestGetIPv4BroadcastAddress(t *testing.T) {
	tests := []struct {
		name  string
		ipNet *net.IPNet
		want  net.IP
	}{
		{
			name:  "awl-default",
			ipNet: getIPNet("10.66.0.1/24"),
			want:  net.IPv4(10, 66, 0, 255).To4(),
		},
		{
			name:  "local-network",
			ipNet: getIPNet("192.168.1.19/24"),
			want:  net.IPv4(192, 168, 1, 255).To4(),
		},
		{
			name:  "docker-network",
			ipNet: getIPNet("172.17.0.1/16"),
			want:  net.IPv4(172, 17, 255, 255).To4(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetIPv4BroadcastAddress(tt.ipNet); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetIPv4BroadcastAddress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func getIPNet(s string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return ipNet
}
