package vpn

import (
	"encoding/binary"
	"math/rand/v2"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv6"
)

// TestPacket_RewriteAddrsMatchesRecalculate compares the incremental update
// with SetSrc/SetDst + RecalculateChecksum on random packets.
func TestPacket_RewriteAddrsMatchesRecalculate(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var udpZero int
	for i := range 3000 {
		v6 := i%2 == 1
		protos := []byte{IPProtocolTCP, IPProtocolUDP, 1, 47} // and ICMP, GRE: IP header only
		if v6 {
			protos = []byte{IPProtocolTCP, IPProtocolUDP, IPProtocolICMPv6}
		}
		proto := protos[r.IntN(len(protos))]
		// no flags, DF, or the first fragment (MF): its transport checksum
		// covers the whole datagram and gets the same update
		fragField := []uint16{0, 0x4000, 0x2000}[r.IntN(3)]
		want := randomPacket(r, v6, proto, fragField)
		got := new(Packet)
		want.CopyTo(got)

		src, dst := randomAddr(r, v6), randomAddr(r, v6)
		switch r.IntN(3) {
		case 1: // gateway forward: src only
			dst = netip.Addr{}
		case 2: // gateway return: dst only
			src = netip.Addr{}
		}
		if src.IsValid() {
			want.SetSrc(src)
		}
		if dst.IsValid() {
			want.SetDst(dst)
		}
		want.RecalculateChecksum()
		got.RewriteAddrs(src, dst)

		if proto == IPProtocolUDP {
			// RecalculateChecksum writes a computed zero as is, RewriteAddrs as 0xffff
			off := transportOffset(want.Packet) + 6
			if binary.BigEndian.Uint16(want.Packet[off:]) == 0 {
				require.Equal(t, uint16(0xffff), binary.BigEndian.Uint16(got.Packet[off:]))
				binary.BigEndian.PutUint16(got.Packet[off:], 0)
				udpZero++
			}
		}
		require.Equal(t, want.Packet, got.Packet, "packet %d: v6=%v proto=%d src=%v dst=%v", i, v6, proto, src, dst)
		require.True(t, checksumsValid(got), "packet %d", i)
	}
	t.Logf("UDP checksum computed as zero: %d", udpZero)
}

func TestPacket_RewriteAddrsUDPZero(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, v6 := range []bool{false, true} {
		// zero means "no checksum" and must stay zero
		p := randomPacket(r, v6, IPProtocolUDP, 0)
		off := transportOffset(p.Packet) + 6
		binary.BigEndian.PutUint16(p.Packet[off:], 0)
		p.RewriteAddrs(randomAddr(r, v6), randomAddr(r, v6))
		require.Zero(t, binary.BigEndian.Uint16(p.Packet[off:]), "v6=%v", v6)
		require.True(t, checksumsValid(p), "v6=%v", v6)

		// a computed zero is sent as 0xffff: change one payload word so that
		// the checksum after the rewrite is zero
		p = randomPacket(r, v6, IPProtocolUDP, 0)
		src, dst := randomAddr(r, v6), randomAddr(r, v6)
		probe := new(Packet)
		p.CopyTo(probe)
		probe.SetSrc(src)
		probe.SetDst(dst)
		probe.RecalculateChecksum()
		word := p.Packet[off+2:]
		sum := uint32(binary.BigEndian.Uint16(word)) + uint32(binary.BigEndian.Uint16(probe.Packet[off:]))
		binary.BigEndian.PutUint16(word, uint16(foldChecksum(sum)))
		p.RecalculateChecksum()

		p.RewriteAddrs(src, dst)
		require.Equal(t, uint16(0xffff), binary.BigEndian.Uint16(p.Packet[off:]), "v6=%v", v6)
		require.True(t, checksumsValid(p), "v6=%v", v6)
	}
}

// TestPacket_RewriteAddrsFragments rewrites every fragment of a datagram and
// checks the reassembled datagram: the first fragment carries the transport
// checksum of the whole datagram, the others must not change beyond the IPv4
// header.
func TestPacket_RewriteAddrsFragments(t *testing.T) {
	cases := []struct {
		name     string
		v6       bool
		proto    byte
		checksum int // offset of the transport checksum
	}{
		{"IPv4 UDP", false, IPProtocolUDP, 6},
		{"IPv6 UDP", true, IPProtocolUDP, 6},
		{"IPv6 ICMPv6", true, IPProtocolICMPv6, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rand.New(rand.NewPCG(5, 6))
			ipHdr := 20
			src, dst := netip.MustParseAddr("10.66.0.7"), netip.MustParseAddr("10.66.0.1")
			if tc.v6 {
				ipHdr = 40
				src, dst = netip.MustParseAddr("fd00:66::7"), netip.MustParseAddr("fd00:66::1")
			}
			const segmentLen, chunk = 3008, 1000
			whole := new(Packet)
			pkt := whole.Buffer[tunPacketOffset : tunPacketOffset+ipHdr+segmentLen]
			for i := range pkt {
				pkt[i] = byte(r.Uint32())
			}
			if tc.v6 {
				pkt[0] = 0x60
				binary.BigEndian.PutUint16(pkt[4:], segmentLen)
				pkt[6] = tc.proto
			} else {
				pkt[0] = 0x45
				binary.BigEndian.PutUint16(pkt[2:], uint16(len(pkt)))
				binary.BigEndian.PutUint16(pkt[6:], 0)
				pkt[9] = tc.proto
			}
			if tc.proto == IPProtocolUDP {
				binary.BigEndian.PutUint16(pkt[ipHdr+4:], segmentLen)
			}
			whole.Packet = pkt
			require.True(t, whole.Parse())
			whole.RecalculateChecksum()
			datagram := append([]byte(nil), pkt[ipHdr:]...)

			var reassembled []byte
			for off := 0; off < len(datagram); off += chunk {
				body := datagram[off:min(off+chunk, len(datagram))]
				more := off+chunk < len(datagram)
				frag := new(Packet)
				var fp []byte
				if tc.v6 {
					fp = frag.Buffer[tunPacketOffset : tunPacketOffset+ipHdr+ipv6FragmentHeaderLen+len(body)]
					copy(fp, pkt[:ipHdr])
					binary.BigEndian.PutUint16(fp[4:], uint16(ipv6FragmentHeaderLen+len(body)))
					fp[6] = ipv6NextHeaderFragment
					fh := fp[ipHdr:]
					fh[0], fh[1] = tc.proto, 0
					offsetField := uint16(off)
					if more {
						offsetField |= 1
					}
					binary.BigEndian.PutUint16(fh[2:], offsetField)
					binary.BigEndian.PutUint32(fh[4:], 0x12345678)
					copy(fh[ipv6FragmentHeaderLen:], body)
				} else {
					fp = frag.Buffer[tunPacketOffset : tunPacketOffset+ipHdr+len(body)]
					copy(fp, pkt[:ipHdr])
					copy(fp[ipHdr:], body)
					binary.BigEndian.PutUint16(fp[2:], uint16(len(fp)))
					flags := uint16(off / 8)
					if more {
						flags |= 0x2000
					}
					binary.BigEndian.PutUint16(fp[6:], flags)
					binary.BigEndian.PutUint16(fp[10:], 0)
					binary.BigEndian.PutUint16(fp[10:], checksumIPv4Header(fp[:ipHdr]))
				}
				frag.Packet = fp
				require.True(t, frag.Parse())
				before := append([]byte(nil), fp...)

				frag.RewriteAddrs(src, dst)
				gotBody := fp[len(fp)-len(body):]
				if !tc.v6 {
					require.Zero(t, checksumIPv4Header(fp[:ipHdr]), "IPv4 header checksum of fragment at %d", off)
				}
				if off > 0 {
					require.Equal(t, before[ipHdr:], fp[ipHdr:], "fragment at %d changed beyond the IP header", off)
				}
				reassembled = append(reassembled, gotBody...)
			}
			c := tc.checksum
			require.Equal(t, datagram[:c], reassembled[:c], "transport header changed")
			require.Equal(t, datagram[c+2:], reassembled[c+2:], "payload changed")
			pseudo := pseudoHeaderSum(src.AsSlice(), dst.AsSlice(), tc.proto, len(reassembled))
			require.Equal(t, uint16(0xffff), onesSum(reassembled, pseudo), "checksum of the reassembled datagram")
		})
	}

	// a truncated Fragment header must not panic
	p := new(Packet)
	pkt := p.Buffer[tunPacketOffset : tunPacketOffset+ipv6.HeaderLen+4]
	pkt[0], pkt[6] = 0x60, ipv6NextHeaderFragment
	p.Packet = pkt
	require.True(t, p.Parse())
	require.NotPanics(t, func() { p.RewriteAddrs(netip.MustParseAddr("fd00:66::7"), netip.MustParseAddr("fd00:66::1")) })
}

func randomPacket(r *rand.Rand, v6 bool, proto byte, fragField uint16) *Packet {
	p := new(Packet)
	hdr := 40
	if !v6 {
		hdr = 20 + 4*r.IntN(3) // with and without options
	}
	transportHdr := map[byte]int{IPProtocolTCP: 20, IPProtocolUDP: 8, IPProtocolICMPv6: 8}[proto]
	total := hdr + transportHdr + 2 + r.IntN(1400)
	pkt := p.Buffer[tunPacketOffset : tunPacketOffset+total]
	for i := range pkt {
		pkt[i] = byte(r.Uint32())
	}
	if v6 {
		pkt[0] = 0x60
		binary.BigEndian.PutUint16(pkt[4:], uint16(total-40))
		pkt[6] = proto
	} else {
		pkt[0] = 0x40 | byte(hdr/4)
		binary.BigEndian.PutUint16(pkt[2:], uint16(total))
		binary.BigEndian.PutUint16(pkt[6:], fragField)
		pkt[9] = proto
	}
	p.Packet = pkt
	if !p.Parse() {
		panic("parse")
	}
	p.RecalculateChecksum()
	return p
}

func randomAddr(r *rand.Rand, v6 bool) netip.Addr {
	if v6 {
		var b [16]byte
		binary.BigEndian.PutUint64(b[:], r.Uint64())
		binary.BigEndian.PutUint64(b[8:], r.Uint64())
		return netip.AddrFrom16(b)
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], r.Uint32())
	return netip.AddrFrom4(b)
}

func transportOffset(pkt []byte) int {
	if pkt[0]>>4 == 6 {
		return 40
	}
	return int(pkt[0]&0x0f) << 2
}

// checksumsValid verifies the IPv4 header checksum and the TCP/UDP/ICMPv6
// checksum of an unfragmented packet, independently of the code under test.
func checksumsValid(p *Packet) bool {
	pkt := p.Packet
	off := transportOffset(pkt)
	if !p.IsIPv6 && onesSum(pkt[:off], 0) != 0xffff {
		return false
	}
	segment := pkt[off:]
	switch p.IPProtocol {
	case IPProtocolTCP, IPProtocolUDP:
	case IPProtocolICMPv6:
		if !p.IsIPv6 {
			return true
		}
	default:
		return true
	}
	if p.IPProtocol == IPProtocolUDP && binary.BigEndian.Uint16(segment[6:]) == 0 {
		return true // no checksum
	}
	return onesSum(segment, pseudoHeaderSum(p.Src, p.Dst, p.IPProtocol, len(segment))) == 0xffff
}

func pseudoHeaderSum(src, dst []byte, proto byte, length int) uint32 {
	sum := uint32(onesSum(src, 0)) + uint32(onesSum(dst, 0))
	return sum + uint32(proto) + uint32(length>>16) + uint32(length&0xffff)
}

// onesSum is the folded one's complement sum of b as big-endian 16-bit words
// plus sum.
func onesSum(b []byte, sum uint32) uint16 {
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	return uint16(foldChecksum(sum))
}
