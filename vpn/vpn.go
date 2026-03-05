package vpn

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ipfs/go-log/v2"
	"go.uber.org/zap"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/anywherelan/awl/metrics"
)

const (
	InterfaceMTU   = 3500
	maxContentSize = InterfaceMTU + 100
	// internal tun header. see offset in tun_darwin (4) and tun_linux (virtioNetHdrLen, currently 10)
	tunPacketOffset = 14
)

type Device struct {
	tun     tun.Device
	mtu     int64
	localIP net.IP

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
		tun:     tunDevice,
		mtu:     int64(realMtu),
		localIP: localIP,
		packetsPool: sync.Pool{
			New: func() interface{} {
				return new(Packet)
			}},
		logger: log.Logger("awl/vpn"),
	}
	go dev.tunEventsReader()

	return dev, nil
}

func (d *Device) GetTempPacket() *Packet {
	return d.packetsPool.Get().(*Packet)
}

func (d *Device) PutTempPacket(data *Packet) {
	data.clear()
	d.packetsPool.Put(data)
}

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

// bufs should be len(bufs) == 0, cap(bufs) == len(packets)
func (d *Device) WritePacketsBatch(packets []*Packet, bufs [][]byte, senderIP net.IP) error {
	for _, packet := range packets {
		if packet.IsIPv6 {
			// TODO: implement. We need to set Device.localIP ipv6 instead of ipv4
			continue
		} else {
			copy(packet.Src, senderIP)
			copy(packet.Dst, d.localIP)
		}
		packet.RecalculateChecksum()

		bufs = append(bufs, packet.Buffer[:tunPacketOffset+len(packet.Packet)])
	}

	defer func() {
		for i := range bufs {
			bufs[i] = nil
		}
	}()

	packetsCount, err := d.tun.Write(bufs, tunPacketOffset)
	if err != nil {
		metrics.VPNTunWriteErrorsTotal.Inc()
		return fmt.Errorf("write packet to tun: %v", err)
	} else if packetsCount < len(bufs) {
		d.logger.Warnf("wrote %d packets, but expected %d", packetsCount, len(bufs))
	}

	return nil
}

func (d *Device) BatchSize() int {
	return d.tun.BatchSize()
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

func (d *Device) ReadTUNPackets(packetsHandler func([]*Packet)) {
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
				d.logger.Error("Failed to parse packet",
					zap.ByteString("packet", data.Packet[:min(10, len(data.Packet))]),
				)

				packets[i] = nil
				d.PutTempPacket(data)
				continue
			}
		}

		if packetsCount > 0 {
			// packetsHandler skips nil packets in slice and sets packet to nil after successful processing
			packetsHandler(packets[:packetsCount])
		}

		if errors.Is(err, tun.ErrTooManySegments) {
			continue
		} else if errors.Is(err, os.ErrClosed) {
			return
		} else if err != nil {
			metrics.VPNTunReadErrorsTotal.Inc()
			d.logger.Errorf("Failed to read packets from TUN device: %v", err)
			return
		}
	}
}
