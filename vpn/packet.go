package vpn

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/device"
)

const (
	IPProtocolTCP    = 6
	IPProtocolUDP    = 17
	IPProtocolICMPv6 = 58

	ipv4offsetChecksum = 10

	ipv6NextHeaderFragment = 44
	ipv6FragmentHeaderLen  = 8
)

type Packet struct {
	Buffer     [maxContentSize]byte
	Packet     []byte
	Src        net.IP
	Dst        net.IP
	IsIPv6     bool
	GatewayDir GatewayDir
	IPProtocol byte
}

// GatewayDir tags a tunnel packet's intent with respect to VPN gateway mode.
type GatewayDir uint8

const (
	// GatewayDirNone is a regular awl peer-to-peer packet. Receiver applies the
	// standard full src/dst rewrite.
	GatewayDirNone GatewayDir = iota
	// GatewayDirForward is sent client → server: the sender asks the receiver,
	// who must be acting as a VPN gateway server for the sender, to forward
	// this packet to the internet via NAT. Receiver rewrites only src.
	GatewayDirForward
	// GatewayDirReturn is sent server → client: the sender (the gateway server)
	// is delivering a NAT-returned packet from the internet back to the client.
	// Receiver rewrites only dst.
	GatewayDirReturn
)

// Buf returns the slice of data.Buffer that includes the TUN header offset and
// the parsed packet body, ready to be appended into a bufs slice for tun.Write.
func (data *Packet) Buf() []byte {
	return data.Buffer[:tunPacketOffset+len(data.Packet)]
}

func (data *Packet) clear() {
	data.Packet = nil
	data.Src = nil
	data.Dst = nil
	data.IsIPv6 = false
	data.IPProtocol = 0
	data.GatewayDir = GatewayDirNone
}

func (data *Packet) CopyTo(copyPacket *Packet) {
	*copyPacket = *data
	// Buffer is an array copied by value above, but Packet/Src/Dst are slice
	// headers still aliasing data.Buffer. Re-derive them as sub-slices of the
	// copy's own buffer.
	copyPacket.Packet = copyPacket.Buffer[tunPacketOffset : len(data.Packet)+tunPacketOffset]
	if data.Src != nil {
		copyPacket.setAddrs()
	}
}

func (data *Packet) ReadFrom(stream io.Reader) (int64, error) {
	totalRead := tunPacketOffset
	for {
		n, err := stream.Read(data.Buffer[totalRead:])
		totalRead += n
		if err == io.EOF {
			data.Packet = data.Buffer[tunPacketOffset:totalRead]
			return int64(totalRead - tunPacketOffset), nil
		} else if err != nil {
			return int64(totalRead - tunPacketOffset), err
		}
		// The buffer is sized with slack above the MTU (maxContentSize), so a
		// valid single-packet body never fills it. A full buffer means an
		// oversized frame; reject it instead of reading into the empty tail
		// (which would spin on (0, nil) reads).
		if totalRead == len(data.Buffer) {
			return int64(totalRead - tunPacketOffset), fmt.Errorf("packet body exceeds max size %d bytes", MaxPacketBodySize)
		}
	}
}

func (data *Packet) Parse() bool {
	if len(data.Packet) == 0 {
		return false
	}

	packet := data.Packet
	switch version := packet[0] >> 4; version {
	case ipv4.Version:
		if len(packet) < ipv4.HeaderLen {
			return false
		}
		// Validate the header length against the actual packet so downstream
		// slicing (RewriteAddrs, RecalculateChecksum) can't run past the packet
		// and panic. Transport-length is not enforced here (it would drop legit
		// non-first IP fragments); RewriteAddrs and RecalculateChecksum guard
		// their own transport slicing.
		ipHeaderLen := int(packet[0]&0x0f) << 2
		if ipHeaderLen < ipv4.HeaderLen || ipHeaderLen > len(packet) {
			return false
		}
		data.IPProtocol = packet[9]

		data.IsIPv6 = false
		data.setAddrs()
	case ipv6.Version:
		if len(packet) < ipv6.HeaderLen {
			return false
		}

		data.IsIPv6 = true
		data.setAddrs()
		// IPv6 Next Header field is at offset 6.
		data.IPProtocol = packet[6]
	default:
		return false
	}

	return true
}

// RecalculateChecksum recomputes the IPv4 header checksum and the transport
// checksum over the whole packet. Only tests use it, to build packets: awl
// rewrites received packets with RewriteAddrs.
func (data *Packet) RecalculateChecksum() {
	if data.IsIPv6 {
		data.recalculateChecksumIPv6()
		return
	}
	// Guard against malformed lengths so a bad packet can't slice past bounds and
	// panic, even if RecalculateChecksum is called without a prior valid Parse.
	ipHeaderLen := int(data.Packet[0]&0x0f) << 2
	if ipHeaderLen < ipv4.HeaderLen || ipHeaderLen > len(data.Packet) {
		return
	}
	copy(data.Packet[ipv4offsetChecksum:], []byte{0, 0})
	ipChecksum := checksumIPv4Header(data.Packet[:ipHeaderLen])
	binary.BigEndian.PutUint16(data.Packet[ipv4offsetChecksum:], ipChecksum)

	switch protocol := data.Packet[9]; protocol {
	case IPProtocolTCP:
		if len(data.Packet) < ipHeaderLen+18 {
			return
		}
		tcpOffsetChecksum := ipHeaderLen + 16
		copy(data.Packet[tcpOffsetChecksum:], []byte{0, 0})
		checksum := checksumIPv4TCPUDP(data.Packet[ipHeaderLen:], uint32(protocol), data.Src, data.Dst)
		binary.BigEndian.PutUint16(data.Packet[tcpOffsetChecksum:], checksum)
	case IPProtocolUDP:
		if len(data.Packet) < ipHeaderLen+8 {
			return
		}
		udpOffsetChecksum := ipHeaderLen + 6
		copy(data.Packet[udpOffsetChecksum:], []byte{0, 0})
		checksum := checksumIPv4TCPUDP(data.Packet[ipHeaderLen:], uint32(protocol), data.Src, data.Dst)
		binary.BigEndian.PutUint16(data.Packet[udpOffsetChecksum:], checksum)
	}
}

// recalculateChecksumIPv6 is RecalculateChecksum for IPv6. IPv6 has no
// IP-header checksum (unlike IPv4), but the TCP/UDP pseudo-header includes the
// 128-bit src and dst addresses, so an address rewrite invalidates the
// transport checksum.
//
// IPv6 extension headers are NOT walked: the Next Header at offset 6 must name
// the transport protocol. Unlike RewriteAddrs, it does not handle fragments.
func (data *Packet) recalculateChecksumIPv6() {
	if len(data.Packet) < ipv6.HeaderLen {
		return
	}
	payload := data.Packet[ipv6.HeaderLen:]
	switch data.IPProtocol {
	case IPProtocolTCP:
		if len(payload) < 18 {
			return
		}
		copy(payload[16:18], []byte{0, 0})
		checksum := checksumIPv6TCPUDP(payload, uint32(data.IPProtocol), data.Src, data.Dst)
		binary.BigEndian.PutUint16(payload[16:], checksum)
	case IPProtocolUDP:
		if len(payload) < 8 {
			return
		}
		copy(payload[6:8], []byte{0, 0})
		checksum := checksumIPv6TCPUDP(payload, uint32(data.IPProtocol), data.Src, data.Dst)
		binary.BigEndian.PutUint16(payload[6:], checksum)
	case IPProtocolICMPv6:
		if len(payload) < 4 {
			return
		}
		copy(payload[2:4], []byte{0, 0})
		checksum := checksumIPv6TCPUDP(payload, uint32(data.IPProtocol), data.Src, data.Dst)
		binary.BigEndian.PutUint16(payload[2:], checksum)
	}
}

// SrcAddr returns the parsed source address without allocating. Its family
// follows the IP header: an IPv4-mapped source of an IPv6 packet stays an IPv6
// address.
func (data *Packet) SrcAddr() netip.Addr {
	addr, _ := netip.AddrFromSlice(data.Src)
	return addr
}

// DstAddr is SrcAddr for the destination address.
func (data *Packet) DstAddr() netip.Addr {
	addr, _ := netip.AddrFromSlice(data.Dst)
	return addr
}

// SetSrc writes addr into the source address of the parsed packet without
// allocating. addr must be of the packet's IP version: in an IPv4 packet any
// other address is ignored, in an IPv6 packet an IPv4 one is written mapped.
// The checksums are not updated, see RewriteAddrs and RecalculateChecksum.
func (data *Packet) SetSrc(addr netip.Addr) {
	putAddr(data.Src, addr)
}

// SetDst is SetSrc for the destination address.
func (data *Packet) SetDst(addr netip.Addr) {
	putAddr(data.Dst, addr)
}

// RewriteAddrs writes src and dst into the parsed packet like SetSrc and
// SetDst, and updates the checksums incrementally (RFC 1624). An invalid
// address keeps the field.
//
// Only the address words change, so the IPv4 header checksum and the
// TCP/UDP/ICMPv6 checksum, whose pseudo-header covers the addresses, are
// adjusted by their difference and the payload is not read. The result is the
// same as a full recompute when the incoming checksums are valid; a wrong
// incoming checksum stays wrong.
//
// Only the first fragment carries the transport header. Its checksum covers
// the whole datagram and gets the same update. Later IPv4 fragments get only
// the IP header checksum updated, later IPv6 fragments are not changed. In
// IPv6 only a Fragment header right after the IPv6 header is passed; other
// extension headers are not walked.
func (data *Packet) RewriteAddrs(src, dst netip.Addr) {
	var old [2 * net.IPv6len]byte
	srcLen := copy(old[:], data.Src)
	dstLen := copy(old[srcLen:], data.Dst)
	if src.IsValid() {
		putAddr(data.Src, src)
	}
	if dst.IsValid() {
		putAddr(data.Dst, dst)
	}
	delta := addChecksumDelta(0, old[:srcLen], data.Src)
	delta = addChecksumDelta(delta, old[srcLen:srcLen+dstLen], data.Dst)
	delta = foldChecksum(delta)
	// 0xffff is zero in one's complement: the addresses did not change the sum
	if delta == 0 || delta == 0xffff {
		return
	}

	packet := data.Packet
	protocol := data.IPProtocol
	var transport []byte
	if data.IsIPv6 {
		var ok bool
		if protocol, transport, ok = findIPv6Transport(packet, protocol); !ok {
			return
		}
	} else {
		ipHeaderLen := int(packet[0]&0x0f) << 2
		if ipHeaderLen < ipv4.HeaderLen || ipHeaderLen > len(packet) {
			return
		}
		adjustChecksum(packet[ipv4offsetChecksum:], delta, false)
		const fragmentOffsetMask = 0x1fff
		if binary.BigEndian.Uint16(packet[6:])&fragmentOffsetMask != 0 {
			return
		}
		transport = packet[ipHeaderLen:]
	}

	switch protocol {
	case IPProtocolTCP:
		if len(transport) >= 18 {
			adjustChecksum(transport[16:], delta, false)
		}
	case IPProtocolUDP:
		if len(transport) >= 8 {
			adjustChecksum(transport[6:], delta, true)
		}
	case IPProtocolICMPv6:
		if data.IsIPv6 && len(transport) >= 4 {
			adjustChecksum(transport[2:], delta, false)
		}
	}
}

// findIPv6Transport returns the transport protocol and header that follow the
// IPv6 header and an optional Fragment header. ok is false for a later fragment,
// which has no transport header.
func findIPv6Transport(packet []byte, nextHeader byte) (protocol byte, transport []byte, ok bool) {
	transport = packet[ipv6.HeaderLen:]
	if nextHeader != ipv6NextHeaderFragment {
		return nextHeader, transport, true
	}
	const fragmentOffsetMask = 0xfff8
	if len(transport) < ipv6FragmentHeaderLen || binary.BigEndian.Uint16(transport[2:])&fragmentOffsetMask != 0 {
		return 0, nil, false
	}
	return transport[0], transport[ipv6FragmentHeaderLen:], true
}

// addChecksumDelta adds ~old + new for each 16-bit word of an address to sum.
// old and new have the same length.
func addChecksumDelta(sum uint32, old, new []byte) uint32 {
	for i := 0; i+1 < len(new); i += 2 {
		sum += uint32(^binary.BigEndian.Uint16(old[i:]))
		sum += uint32(binary.BigEndian.Uint16(new[i:]))
	}
	return sum
}

func foldChecksum(sum uint32) uint32 {
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return sum
}

// adjustChecksum applies RFC 1624 eqn. 3, HC' = ~(~HC + ~m + m'), to the
// checksum in field[:2]; delta is the folded sum of ~m + m'.
//
// For UDP a zero checksum means "no checksum" (RFC 768) and stays zero, and a
// computed zero is sent as 0xffff (RFC 768, RFC 8200 §8.1).
func adjustChecksum(field []byte, delta uint32, udp bool) {
	checksum := binary.BigEndian.Uint16(field)
	if udp && checksum == 0 {
		return
	}
	checksum = ^uint16(foldChecksum(uint32(^checksum) + delta))
	if udp && checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(field, checksum)
}

func putAddr(dst net.IP, addr netip.Addr) {
	switch len(dst) {
	case net.IPv4len:
		if addr.Is4() {
			b := addr.As4()
			copy(dst, b[:])
		}
	case net.IPv6len:
		b := addr.As16()
		copy(dst, b[:])
	}
}

func (data *Packet) setAddrs() {
	if data.IsIPv6 {
		data.Src = data.Packet[device.IPv6offsetSrc : device.IPv6offsetSrc+net.IPv6len]
		data.Dst = data.Packet[device.IPv6offsetDst : device.IPv6offsetDst+net.IPv6len]
	} else {
		data.Src = data.Packet[device.IPv4offsetSrc : device.IPv4offsetSrc+net.IPv4len]
		data.Dst = data.Packet[device.IPv4offsetDst : device.IPv4offsetDst+net.IPv4len]
	}
}

func checksumIPv4Header(buf []byte) uint16 {
	var v uint32
	for i := 0; i < len(buf)-1; i += 2 {
		v += uint32(binary.BigEndian.Uint16(buf[i:]))
	}
	if len(buf)%2 == 1 {
		v += uint32(buf[len(buf)-1]) << 8
	}
	for v > 0xffff {
		v = (v >> 16) + (v & 0xffff)
	}

	return ^uint16(v)
}

func checksumIPv4TCPUDP(headerAndPayload []byte, protocol uint32, srcIP net.IP, dstIP net.IP) uint16 {
	var csum uint32
	csum += (uint32(srcIP[0]) + uint32(srcIP[2])) << 8
	csum += uint32(srcIP[1]) + uint32(srcIP[3])
	csum += (uint32(dstIP[0]) + uint32(dstIP[2])) << 8
	csum += uint32(dstIP[1]) + uint32(dstIP[3])

	totalLen := uint32(len(headerAndPayload))

	csum += protocol
	csum += totalLen & 0xffff
	csum += totalLen >> 16

	return tcpipChecksum(headerAndPayload, csum)
}

// checksumIPv6TCPUDP computes the TCP/UDP transport checksum using the IPv6
// pseudo-header as defined in RFC 2460 §8.1. The pseudo-header fields are:
//
//	source address       (16 bytes)
//	destination address  (16 bytes)
//	upper-layer length   (4 bytes, big-endian)
//	zero padding         (3 bytes)
//	next header          (1 byte)
//
// headerAndPayload is the transport segment (TCP/UDP header + payload).
// srcIP and dstIP must each be 16 bytes (net.IPv6len).
func checksumIPv6TCPUDP(headerAndPayload []byte, protocol uint32, srcIP net.IP, dstIP net.IP) uint16 {
	var csum uint32
	// Source address
	for i := 0; i < net.IPv6len; i += 2 {
		csum += uint32(srcIP[i])<<8 + uint32(srcIP[i+1])
	}
	// Destination address
	for i := 0; i < net.IPv6len; i += 2 {
		csum += uint32(dstIP[i])<<8 + uint32(dstIP[i+1])
	}
	// Upper-layer packet length (same as transport segment length)
	totalLen := uint32(len(headerAndPayload))
	csum += totalLen >> 16
	csum += totalLen & 0xffff
	// Next Header (protocol)
	csum += protocol

	return tcpipChecksum(headerAndPayload, csum)
}

// Calculate the TCP/IP checksum defined in rfc1071. The passed-in csum is any
// initial checksum data that's already been computed.
// Borrowed from google/gopacket
func tcpipChecksum(data []byte, csum uint32) uint16 {
	// to handle odd lengths, we loop to length - 1, incrementing by 2, then
	// handle the last byte specifically by checking against the original
	// length.
	length := len(data) - 1
	for i := 0; i < length; i += 2 {
		// For our test packet, doing this manually is about 25% faster
		// (740 ns vs. 1000ns) than doing it by calling binary.BigEndian.Uint16.
		csum += uint32(data[i]) << 8
		csum += uint32(data[i+1])
	}
	if len(data)%2 == 1 {
		csum += uint32(data[length]) << 8
	}
	for csum > 0xffff {
		csum = (csum >> 16) + (csum & 0xffff)
	}
	return ^uint16(csum)
}

// GetIPv4BroadcastAddress returns the broadcast address of an IPv4 prefix: the
// network address with all host bits set.
func GetIPv4BroadcastAddress(prefix netip.Prefix) netip.Addr {
	network := prefix.Masked().Addr().As4()
	hostBits := ^uint32(0) >> prefix.Bits()
	var broadcast [4]byte
	binary.BigEndian.PutUint32(broadcast[:], binary.BigEndian.Uint32(network[:])|hostBits)

	return netip.AddrFrom4(broadcast)
}
