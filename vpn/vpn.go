package vpn

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/ipfs/go-log/v2"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	interfaceMTU  = 3500
	outboundChCap = 50
	// internal tun header
	tunPacketOffset = 4
)

type Device struct {
	tun           tun.Device
	interfaceName string
	mtu           int64
	localIP       net.IP
	outboundCh    chan *Packet

	outboundDataPool sync.Pool
	logger           *log.ZapEventLogger
}

func NewDevice(interfaceName string, localIP net.IP, ipMask net.IPMask) (*Device, error) {
	tunDevice, err := newTUN(interfaceName, interfaceMTU, localIP, ipMask)
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN device: %v", err)
	}

	realInterfaceName, err := tunDevice.Name()
	if err != nil {
		return nil, fmt.Errorf("failed to get TUN interface name: %v", err)
	}

	realMtu, err := tunDevice.MTU()
	if err != nil {
		return nil, fmt.Errorf("failed to get TUN mtu: %v", err)
	}

	dev := &Device{
		tun:           tunDevice,
		interfaceName: realInterfaceName,
		mtu:           int64(realMtu),
		localIP:       localIP,
		outboundCh:    make(chan *Packet, outboundChCap),
		outboundDataPool: sync.Pool{
			New: func() interface{} {
				return new(Packet)
			}},
		logger: log.Logger("awl/vpn"),
	}
	go dev.tunEventsReader()
	go dev.tunPacketsReader()

	return dev, nil
}

func (d *Device) GetTempPacket() *Packet {
	return d.outboundDataPool.Get().(*Packet)
}

func (d *Device) PutTempPacket(data *Packet) {
	data.clear()
	d.outboundDataPool.Put(data)
}

func (d *Device) WritePacket(data *Packet, senderIP net.IP) error {
	if data.IsIPv6 {
		// TODO: implement. We need to set Device.localIP ipv6 instead of ipv4
		return nil
	} else {
		copy(data.Src, senderIP)
		copy(data.Dst, d.localIP)
	}

	_, err := d.tun.Write(data.Buffer[:tunPacketOffset+len(data.Packet)], tunPacketOffset)
	if err != nil {
		return fmt.Errorf("write packet to tun: %v", err)
	}

	return nil
}

func (d *Device) OutboundChan() <-chan *Packet {
	return d.outboundCh
}

func (d *Device) Close() error {
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
			if mtu > device.MaxContentSize {
				tooLarge = fmt.Sprintf(" (too large, capped at %v)", device.MaxContentSize)
				mtu = device.MaxContentSize
			}
			old := atomic.SwapInt64(&d.mtu, int64(mtu))
			if int(old) != mtu {
				d.logger.Infof("MTU updated: %v%s", mtu, tooLarge)
			}
		}

		if event&tun.EventUp != 0 {
			//d.logger.Infof("Interface up requested")
			// TODO
		}

		if event&tun.EventDown != 0 {
			d.logger.Infof("Interface down requested")
			// TODO
		}
	}
}

func (d *Device) tunPacketsReader() {
	var data *Packet
	for {
		if data == nil {
			data = d.GetTempPacket()
		} else {
			data.clear()
		}

		size, err := d.tun.Read(data.Buffer[:], tunPacketOffset)
		if err != nil {
			d.logger.Errorf("Failed to read packet from TUN device: %v", err)
			return
		}
		if size == 0 || size > device.MaxContentSize {
			continue
		}

		data.Packet = data.Buffer[tunPacketOffset : size+tunPacketOffset]
		okay := data.Parse()
		if !okay {
			continue
		}

		d.outboundCh <- data
		data = nil
	}
}

type Packet struct {
	Buffer [device.MaxContentSize]byte
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
