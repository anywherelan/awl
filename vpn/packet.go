package vpn

import (
	"encoding/binary"
	"io"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/device"
)

const (
	IPProtocolTCP = 6
	IPProtocolUDP = 17

	ipv4offsetChecksum = 10
)

type Packet struct {
	Buffer     [maxContentSize]byte
	Packet     []byte
	Src        net.IP
	Dst        net.IP
	IsIPv6     bool
	IPProtocol byte
}

func (data *Packet) clear() {
	data.Packet = nil
	data.Src = nil
	data.Dst = nil
	data.IsIPv6 = false
	data.IPProtocol = 0
}

func (data *Packet) CopyTo(copyPacket *Packet) {
	*copyPacket = *data
	// set Packet reference to a new buffer
	copyPacket.Packet = copyPacket.Buffer[tunPacketOffset : len(data.Packet)+tunPacketOffset]
}

func (data *Packet) ReadFrom(stream io.Reader) (int64, error) {
	var totalRead = tunPacketOffset
	for {
		n, err := stream.Read(data.Buffer[totalRead:])
		totalRead += n
		if err == io.EOF {
			data.Packet = data.Buffer[tunPacketOffset:totalRead]
			return int64(totalRead - tunPacketOffset), nil
		} else if err != nil {
			return int64(totalRead - tunPacketOffset), err
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

		data.Src = packet[device.IPv4offsetSrc : device.IPv4offsetSrc+net.IPv4len]
		data.Dst = packet[device.IPv4offsetDst : device.IPv4offsetDst+net.IPv4len]
		data.IsIPv6 = false
		data.IPProtocol = data.Packet[9]
	case ipv6.Version:
		if len(packet) < ipv6.HeaderLen {
			return false
		}

		data.Src = packet[device.IPv6offsetSrc : device.IPv6offsetSrc+net.IPv6len]
		data.Dst = packet[device.IPv6offsetDst : device.IPv6offsetDst+net.IPv6len]
		data.IsIPv6 = true
		// TODO: set data.IPProtocol
	default:
		return false
	}

	return true
}

func (data *Packet) RecalculateChecksum() {
	if data.IsIPv6 {
		// TODO
	} else {
		ipHeaderLen := int(data.Packet[0]&0x0f) << 2
		copy(data.Packet[ipv4offsetChecksum:], []byte{0, 0})
		ipChecksum := checksumIPv4Header(data.Packet[:ipHeaderLen])
		binary.BigEndian.PutUint16(data.Packet[ipv4offsetChecksum:], ipChecksum)

		switch protocol := data.Packet[9]; protocol {
		case IPProtocolTCP:
			tcpOffsetChecksum := ipHeaderLen + 16
			copy(data.Packet[tcpOffsetChecksum:], []byte{0, 0})
			checksum := checksumIPv4TCPUDP(data.Packet[ipHeaderLen:], uint32(protocol), data.Src, data.Dst)
			binary.BigEndian.PutUint16(data.Packet[tcpOffsetChecksum:], checksum)
		case IPProtocolUDP:
			udpOffsetChecksum := ipHeaderLen + 6
			copy(data.Packet[udpOffsetChecksum:], []byte{0, 0})
			checksum := checksumIPv4TCPUDP(data.Packet[ipHeaderLen:], uint32(protocol), data.Src, data.Dst)
			binary.BigEndian.PutUint16(data.Packet[udpOffsetChecksum:], checksum)
		}
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

func GetIPv4BroadcastAddress(ipNet *net.IPNet) net.IP {
	ip := make(net.IP, len(ipNet.IP.To4()))
	// calculate broadcast: network | ^mask
	for i := 0; i < len(ipNet.IP.To4()); i++ {
		ip[i] = ipNet.IP[i] | ^ipNet.Mask[i]
	}

	return ip
}
