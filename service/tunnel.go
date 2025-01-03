package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/vpn"
)

const (
	packetHandlersChanCap = 200
)

type Tunnel struct {
	p2p          P2p
	conf         *config.Config
	device       *vpn.Device
	logger       *log.ZapEventLogger
	peersLock    sync.RWMutex
	peerIDToPeer map[peer.ID]*VpnPeer
	netIPToPeer  map[string]*VpnPeer
}

func NewTunnel(p2pService P2p, device *vpn.Device, conf *config.Config) *Tunnel {
	tunnel := &Tunnel{
		p2p:          p2pService,
		conf:         conf,
		device:       device,
		logger:       log.Logger("awl/service/tunnel"),
		peerIDToPeer: make(map[peer.ID]*VpnPeer),
		netIPToPeer:  make(map[string]*VpnPeer),
	}
	tunnel.RefreshPeersList()
	go tunnel.backgroundReadPackets()

	return tunnel
}

func (t *Tunnel) StreamHandler(stream network.Stream) {
	defer func() {
		_ = stream.Close()
	}()

	peerID := stream.Conn().RemotePeer()
	t.peersLock.RLock()
	_, ok := t.peerIDToPeer[peerID]
	t.peersLock.RUnlock()
	if !ok {
		t.logger.Infof("Unknown peer %s tried to tunnel packet", peerID)
		return
	}

	wrappedStream := &io.LimitedReader{}
	for {
		packet := t.device.GetTempPacket()
		packetSize, err := protocol.ReadUint64(stream)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.logger.Warnf("read packet size: %v", err)
			}
			t.device.PutTempPacket(packet)
			return
		}
		wrappedStream.R = stream
		wrappedStream.N = int64(packetSize)
		_, err = packet.ReadFrom(wrappedStream)
		if err != nil {
			t.logger.Warnf("read to packet: %v", err)
			t.device.PutTempPacket(packet)
			return
		}

		t.peersLock.RLock()
		vpnPeer, ok := t.peerIDToPeer[peerID]
		if !ok {
			t.device.PutTempPacket(packet)
			t.peersLock.RUnlock()
			return
		}

		select {
		case vpnPeer.inboundCh <- packet:
		default:
			// TODO: remove log
			t.logger.Warnf("inbound reader dropped packet, len %d", len(packet.Packet))
			t.device.PutTempPacket(packet)
		}
		t.peersLock.RUnlock()
	}
}

func (t *Tunnel) RefreshPeersList() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	t.conf.RLock()
	defer t.conf.RUnlock()
	for _, knownPeer := range t.conf.KnownPeers {
		peerID := knownPeer.PeerId()
		if _, ok := t.peerIDToPeer[peerID]; ok {
			continue
		}
		localIP := net.ParseIP(knownPeer.IPAddr).To4()
		if localIP == nil {
			t.logger.Errorf("Known peer %q has invalid IP %s in conf", knownPeer.DisplayName(), knownPeer.IPAddr)
			return
		}

		vpnPeer := NewVpnPeer(peerID, localIP)
		t.peerIDToPeer[peerID] = vpnPeer
		t.netIPToPeer[string(localIP)] = vpnPeer
		vpnPeer.Start(t)
	}

	for _, vpnPeer := range t.peerIDToPeer {
		_, exists := t.conf.KnownPeers[vpnPeer.peerID.String()]
		if exists {
			continue
		}
		vpnPeer.Close(t)
		delete(t.peerIDToPeer, vpnPeer.peerID)
		delete(t.netIPToPeer, string(vpnPeer.localIP))
	}
}

func (t *Tunnel) Close() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	for _, vpnPeer := range t.peerIDToPeer {
		vpnPeer.Close(t)
		delete(t.peerIDToPeer, vpnPeer.peerID)
		delete(t.netIPToPeer, string(vpnPeer.localIP))
	}
}

func (t *Tunnel) backgroundReadPackets() {
	// TODO: batch read
	for packet := range t.device.OutboundChan() {
		t.peersLock.RLock()
		vpnPeer, ok := t.netIPToPeer[string(packet.Dst)]
		if !ok {
			t.device.PutTempPacket(packet)
			t.peersLock.RUnlock()
			continue
		}

		select {
		case vpnPeer.outboundCh <- packet:
		default:
			t.device.PutTempPacket(packet)
		}
		t.peersLock.RUnlock()
	}
}

func (t *Tunnel) makeTunnelStream(ctx context.Context, peerID peer.ID) (network.Stream, error) {
	err := t.p2p.ConnectPeer(ctx, peerID)
	if err != nil {
		return nil, err
	}

	newStreamFunc := t.p2p.NewStream
	if t.conf.P2pNode.UseDedicatedConnForEachStream {
		newStreamFunc = t.p2p.NewStreamWithDedicatedConn
	}

	stream, err := newStreamFunc(ctx, peerID, protocol.TunnelPacketMethod)
	if err != nil {
		return nil, err
	}

	return stream, nil
}

type VpnPeer struct {
	peerID     peer.ID
	localIP    net.IP
	inboundCh  chan *vpn.Packet
	outboundCh chan *vpn.Packet // from us to remote

	ctx       context.Context
	ctxCancel context.CancelFunc
}

func NewVpnPeer(peerID peer.ID, localIP net.IP) *VpnPeer {
	ctx, cancel := context.WithCancel(context.Background())
	return &VpnPeer{
		peerID:     peerID,
		localIP:    localIP,
		inboundCh:  make(chan *vpn.Packet, packetHandlersChanCap),
		outboundCh: make(chan *vpn.Packet, packetHandlersChanCap),
		ctx:        ctx,
		ctxCancel:  cancel,
	}
}

// TODO: remove Tunnel from VpnPeer dependencies
func (vp *VpnPeer) Start(t *Tunnel) {
	go vp.backgroundInboundHandler(t)

	for i := 0; i < t.conf.P2pNode.ParallelSendingStreamsCount; i++ {
		go vp.backgroundOutboundHandler(t)
	}
}

func (vp *VpnPeer) Close(t *Tunnel) {
	vp.ctxCancel()
	close(vp.inboundCh)
	close(vp.outboundCh)
	for packet := range vp.inboundCh {
		t.device.PutTempPacket(packet)
	}
	for packet := range vp.outboundCh {
		t.device.PutTempPacket(packet)
	}
}

func (vp *VpnPeer) backgroundOutboundHandler(t *Tunnel) {
	const (
		maxPacketsPerStream = 1024 * 1024 * 8 / vpn.InterfaceMTU
		idleStreamTimeout   = 10 * time.Second
		batchSize           = 10
	)
	var (
		stream                  network.Stream
		currentPacketsForStream int
		buffer                  = make([]byte, (batchSize+2)*vpn.InterfaceMTU)
		packets                 = make([]*vpn.Packet, batchSize)
	)
	sendPacket := func(packets []*vpn.Packet) (err error) {
		if stream == nil {
			ctx, cancel := context.WithTimeout(vp.ctx, 2*time.Second)
			stream, err = t.makeTunnelStream(ctx, vp.peerID)
			cancel()
			if err != nil {
				return fmt.Errorf("make tunnel stream: %v", err)
			}
		}

		bytesN := 0
		for _, packet := range packets {
			n := protocol.WritePacketToBuf(buffer[bytesN:], packet.Packet)
			bytesN += n
		}
		_, err = stream.Write(buffer[:bytesN])

		return err
	}

	closeStream := func() {
		if stream != nil {
			_ = stream.Close()
			stream = nil
		}
		currentPacketsForStream = 0
	}

	defer closeStream()
	idleTicker := time.NewTicker(idleStreamTimeout)
	defer idleTicker.Stop()
	for {
		select {
		case packet, open := <-vp.outboundCh:
			if !open {
				return
			}
			if currentPacketsForStream >= maxPacketsPerStream {
				closeStream()
			}

			packets[0] = packet
			packetsBatch := readBatchFromChan(vp.outboundCh, packets, 1)
			currentPacketsForStream += len(packetsBatch)

			err := sendPacket(packetsBatch)
			if err != nil {
				// TODO: remove log
				t.logger.Warnf("send packet to peerID (%s) local ip (%s): %v", vp.peerID, vp.localIP, err)
				closeStream()
			}
			for i := 0; i < len(packetsBatch); i++ {
				t.device.PutTempPacket(packetsBatch[i])
				packetsBatch[i] = nil
			}
		case <-idleTicker.C:
			if len(vp.outboundCh) == 0 {
				closeStream()
			}
		}
	}
}

func (vp *VpnPeer) backgroundInboundHandler(t *Tunnel) {
	for {
		packet, open := <-vp.inboundCh
		if !open {
			return
		}
		ok := packet.Parse()
		if !ok {
			t.logger.Warnf("got invalid packet from peerID (%s) local ip (%s)", vp.peerID, vp.localIP)
			t.device.PutTempPacket(packet)
			continue
		}
		// TODO: add batching
		err := t.device.WritePacket(packet, vp.localIP)
		if err != nil {
			t.logger.Warnf("write packet to vpn: %v", err)
		}

		t.device.PutTempPacket(packet)
	}
}

func readBatchFromChan(ch chan *vpn.Packet, buf []*vpn.Packet, offset int) []*vpn.Packet {
	i := offset
	for {
		if i == len(buf) {
			return buf[:i]
		}
		select {
		case packet, ok := <-ch:
			if !ok {
				return buf[:i]
			}
			buf[i] = packet
			i++
		default:
			return buf[:i]
		}
	}
}
