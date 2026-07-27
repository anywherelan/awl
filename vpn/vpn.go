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
	// MaxPacketBodySize is the largest tunnel packet body (an IP packet, without
	// the internal TUN header offset) that Packet.Buffer can hold. Peers must not
	// send larger frames: protocol.ReadPacketHeader rejects oversized ones up front and
	// Packet.ReadFrom refuses to overflow the buffer.
	MaxPacketBodySize = maxContentSize - tunPacketOffset
)

type Device struct {
	tun      tun.Device
	mtu      int64
	localIP  net.IP
	localIP6 net.IP // may be nil when IPv6 is not configured

	packetsPool sync.Pool
	logger      *log.ZapEventLogger
}

func NewDevice(existingTun tun.Device, interfaceName string, localIP net.IP, ipMask net.IPMask, localIPv6 net.IP, ipMaskv6 net.IPMask) (*Device, error) {
	var tunDevice tun.Device
	var err error
	if existingTun == nil {
		tunDevice, err = newTUN(interfaceName, InterfaceMTU, localIP, ipMask, localIPv6, ipMaskv6)
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
		tun:      tunDevice,
		mtu:      int64(realMtu),
		localIP:  localIP,
		localIP6: localIPv6,
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

// LocalIP returns the awl IPv4 address assigned to this device. Set once in NewDevice.
func (d *Device) LocalIP() net.IP {
	return d.localIP
}

// LocalIP6 returns the awl IPv6 address assigned to this device, or nil if
// IPv6 is not configured. Set once in NewDevice.
func (d *Device) LocalIP6() net.IP {
	return d.localIP6
}

// WriteRawPacket writes a single ready-made IP packet (raw bytes, not a
// *Packet) to the TUN device, framing it with the internal TUN header offset.
// The slice is not retained. Used by the DNS bridge to inject netstack-emitted packets.
func (d *Device) WriteRawPacket(packet []byte) error {
	if len(packet) > MaxPacketBodySize {
		return fmt.Errorf("packet exceeds max body size: %d > %d", len(packet), MaxPacketBodySize)
	}

	data := d.GetTempPacket()
	defer d.PutTempPacket(data)
	n := copy(data.Buffer[tunPacketOffset:], packet)
	data.Packet = data.Buffer[tunPacketOffset : tunPacketOffset+n]

	return d.WriteBufs([][]byte{data.Buf()})
}

// WriteBufs writes a prepared batch of TUN packets in a single tun.Write
// syscall. The caller is responsible for IP rewrites and checksum recalculation
// on the underlying *Packet objects before building bufs via Packet.Buf.
//
// Empty bufs is a no-op. After writing, every entry is set to nil to release
// the underlying buffer for GC; the caller should reuse the same backing array
// (len=0, cap=batchSize) across calls to avoid per-batch allocation.
func (d *Device) WriteBufs(bufs [][]byte) error {
	if len(bufs) == 0 {
		return nil
	}
	defer func() {
		for i := range bufs {
			bufs[i] = nil
		}
	}()

	packetsCount, err := d.tun.Write(bufs, tunPacketOffset)
	if err != nil {
		metrics.VPNTunWriteErrorsTotal.Inc()
		return fmt.Errorf("write packets to tun: %v", err)
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
