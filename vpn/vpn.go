package vpn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ipfs/go-log/v2"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	InterfaceMTU   = 3500
	maxContentSize = InterfaceMTU * 2 // TODO: determine real size
	outboundChCap  = 50
	// internal tun header. see offset in tun_darwin (4) and tun_linux (virtioNetHdrLen, currently 10)
	tunPacketOffset    = 14
	ipv4offsetChecksum = 10
)

type Device struct {
	tun        tun.Device
	mtu        int64
	localIP    net.IP
	outboundCh chan *Packet

	closeCh     chan struct{}
	packetsPool sync.Pool
	logger      *log.ZapEventLogger
}

func NewDevice(existingTun tun.Device, interfaceName string, localIP net.IP, ipMask net.IPMask) (*Device, error) {
	var tunDevice tun.Device
	var err error
	if existingTun == nil {
		tunDevice, err = newTUN(interfaceName, InterfaceMTU, localIP, ipMask)
		if err != nil {
			return nil, fmt.Errorf("failed to create TUN device: %v", err)
		}
	} else {
		tunDevice = existingTun
	}

	realMtu, err := tunDevice.MTU()
	if err != nil {
		return nil, fmt.Errorf("failed to get TUN mtu: %v", err)
	}

	dev := &Device{
		tun:        tunDevice,
		mtu:        int64(realMtu),
		localIP:    localIP,
		outboundCh: make(chan *Packet, outboundChCap),
		packetsPool: sync.Pool{
			New: func() interface{} {
				return new(Packet)
			}},
		logger:  log.Logger("awl/vpn"),
		closeCh: make(chan struct{}),
	}
	go dev.tunEventsReader()
	go dev.tunPacketsReader()

	return dev, nil
}

func (d *Device) GetTempPacket() *Packet {
	return d.packetsPool.Get().(*Packet)
}

func (d *Device) PutTempPacket(data *Packet) {
	data.clear()
	d.packetsPool.Put(data)
}

// TODO: batch write
func (d *Device) WritePacket(data *Packet, senderIP net.IP) error {
	if data.IsIPv6 {
		// TODO: implement. We need to set Device.localIP ipv6 instead of ipv4
		return nil
	} else {
		copy(data.Src, senderIP)
		copy(data.Dst, d.localIP)
	}
	data.RecalculateChecksum()

	bufs := [][]byte{data.Buffer[:tunPacketOffset+len(data.Packet)]}
	packetsCount, err := d.tun.Write(bufs, tunPacketOffset)
	if err != nil {
		return fmt.Errorf("write packet to tun: %v", err)
	} else if packetsCount < len(bufs) {
		d.logger.Warnf("wrote %d packets, len(bufs): %d", packetsCount, len(bufs))
	}

	return nil
}

func (d *Device) OutboundChan() <-chan *Packet {
	return d.outboundCh
}

func (d *Device) Close() error {
	close(d.closeCh)
	return d.tun.Close()
}

func (d *Device) tunEventsReader() {
	for event := range d.tun.Events() {
		if event&tun.EventMTUUpdate != 0 {
			mtu, err := d.tun.MTU()
			if err != nil {
				d.logger.Errorf("Failed to load updated MTU of device: %v", err)
				continue
			}
			if mtu < 0 {
				d.logger.Errorf("MTU not updated to negative value: %v", mtu)
				continue
			}
			var tooLarge string
			if mtu > maxContentSize {
				tooLarge = fmt.Sprintf(" (too large, capped at %v)", maxContentSize)
				mtu = maxContentSize
			}
			old := atomic.SwapInt64(&d.mtu, int64(mtu))
			if int(old) != mtu {
				d.logger.Infof("MTU updated: %v%s", mtu, tooLarge)
			}
		}

		// TODO: check for event&tun.EventUp
		if event&tun.EventDown != 0 {
			d.logger.Infof("Interface down requested")
			// TODO
		}
	}
}

func (d *Device) tunPacketsReader() {
	defer close(d.outboundCh)

	batchSize := d.tun.BatchSize()
	packets := make([]*Packet, batchSize)
	bufs := make([][]byte, batchSize)
	sizes := make([]int, batchSize)

	for {
		for i := range packets {
			if packets[i] == nil {
				packets[i] = d.GetTempPacket()
			} else {
				packets[i].clear()
			}
			bufs[i] = packets[i].Buffer[:]
			sizes[i] = 0
		}

		packetsCount, err := d.tun.Read(bufs, sizes, tunPacketOffset)
		for i := 0; i < packetsCount; i++ {
			size := sizes[i]
			if size == 0 || size > maxContentSize {
				continue
			}

			data := packets[i]
			data.Packet = data.Buffer[tunPacketOffset : size+tunPacketOffset]
			okay := data.Parse()
			if !okay {
				continue
			}

			select {
			case <-d.closeCh:
				return
			case d.outboundCh <- data:
				// ok
			}
			packets[i] = nil
		}

		if errors.Is(err, tun.ErrTooManySegments) {
			continue
		} else if errors.Is(err, os.ErrClosed) {
			return
		} else if err != nil {
			d.logger.Errorf("Failed to read packets from TUN device: %v", err)
			return
		}
	}
}

type Packet struct {
	Buffer [maxContentSize]byte
	Packet []byte
	Src    net.IP
	Dst    net.IP
	IsIPv6 bool
}

func (data *Packet) clear() {
	data.Packet = nil
	data.Src = nil
	data.Dst = nil
	data.IsIPv6 = false
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
	packet := data.Packet
	switch version := packet[0] >> 4; version {
	case ipv4.Version:
		if len(packet) < ipv4.HeaderLen {
			return false
		}

		data.Src = packet[device.IPv4offsetSrc : device.IPv4offsetSrc+net.IPv4len]
		data.Dst = packet[device.IPv4offsetDst : device.IPv4offsetDst+net.IPv4len]
		data.IsIPv6 = false
	case ipv6.Version:
		if len(packet) < ipv6.HeaderLen {
			return false
		}

		data.Src = packet[device.IPv6offsetSrc : device.IPv6offsetSrc+net.IPv6len]
		data.Dst = packet[device.IPv6offsetDst : device.IPv6offsetDst+net.IPv6len]
		data.IsIPv6 = true
	default:
		return false
	}

	return true
}

func (data *Packet) RecalculateChecksum() {
	const (
		IPProtocolTCP = 6
		IPProtocolUDP = 17
	)

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
