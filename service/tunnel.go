package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
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
	p2p    P2p
	conf   *config.Config
	device *vpn.Device
	logger *log.ZapEventLogger

	isClosed         atomic.Bool
	peersLock        sync.RWMutex
	peerIDToPeer     map[peer.ID]*VpnPeer
	netIPToPeer      map[string]*VpnPeer
	udpBroadcastAddr net.IP
}

func NewTunnel(p2pService P2p, device *vpn.Device, conf *config.Config) *Tunnel {
	localIP, netMask := conf.VPNLocalIPMask()
	udpBroadcastAddr := vpn.GetIPv4BroadcastAddress(&net.IPNet{IP: localIP, Mask: netMask})

	tunnel := &Tunnel{
		p2p:              p2pService,
		conf:             conf,
		device:           device,
		logger:           log.Logger("awl/service/tunnel"),
		peerIDToPeer:     make(map[peer.ID]*VpnPeer),
		netIPToPeer:      make(map[string]*VpnPeer),
		udpBroadcastAddr: udpBroadcastAddr,
	}
	tunnel.RefreshPeersList()

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
			// REMOVE
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
		newLocalIP := net.ParseIP(knownPeer.IPAddr).To4()
		if newLocalIP == nil {
			t.logger.Errorf("Known peer %q has invalid IP %s in conf", knownPeer.DisplayName(), knownPeer.IPAddr)
			continue
		}

		prevPeer, exists := t.peerIDToPeer[peerID]
		if exists {
			oldLocalIP := *prevPeer.localIP.Load()
			if oldLocalIP.Equal(newLocalIP) {
				// no changes
				continue
			}

			if !oldLocalIP.Equal(newLocalIP) {
				// changed IP
				delete(t.netIPToPeer, string(oldLocalIP))
				prevPeer.localIP.Store(&newLocalIP)
				t.netIPToPeer[string(newLocalIP)] = prevPeer

				continue
			}

			// impossible case
			continue
		}

		// add new peer
		vpnPeer := NewVpnPeer(peerID, newLocalIP)
		t.peerIDToPeer[peerID] = vpnPeer
		t.netIPToPeer[string(newLocalIP)] = vpnPeer
		vpnPeer.Start(t)
	}

	// delete unknown peers
	for _, vpnPeer := range t.peerIDToPeer {
		_, exists := t.conf.KnownPeers[vpnPeer.peerID.String()]
		if exists {
			continue
		}
		localIP := *vpnPeer.localIP.Load()
		vpnPeer.Close(t)
		delete(t.peerIDToPeer, vpnPeer.peerID)
		delete(t.netIPToPeer, string(localIP))
	}
}

func (t *Tunnel) Close() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	t.isClosed.Store(true)

	for _, vpnPeer := range t.peerIDToPeer {
		localIP := *vpnPeer.localIP.Load()
		vpnPeer.Close(t)
		delete(t.peerIDToPeer, vpnPeer.peerID)
		delete(t.netIPToPeer, string(localIP))
	}
}

// HandleReadPackets for successfully handled packets it sets packet in slice as nil
func (t *Tunnel) HandleReadPackets(packets []*vpn.Packet) {
	t.peersLock.RLock()
	defer t.peersLock.RUnlock()

	if t.isClosed.Load() {
		return
	}

	for i, packet := range packets {
		if packet == nil {
			continue
		}

		// TODO: ipv6 support
		if packet.Dst.Equal(t.udpBroadcastAddr) || packet.Dst.Equal(net.IPv4bcast) {
			// udp broadcast

			for _, vpnPeer := range t.netIPToPeer {
				// TODO: replace with event-based check OnConnected/OnDisconnected to improve performance
				if !t.p2p.IsConnected(vpnPeer.peerID) {
					continue
				}

				copyPacket := t.device.GetTempPacket()
				packet.CopyTo(copyPacket)

				select {
				case vpnPeer.outboundCh <- copyPacket:
				default:
					t.device.PutTempPacket(copyPacket)
				}
			}

			continue
		}

		vpnPeer, ok := t.netIPToPeer[string(packet.Dst)]
		if !ok {
			continue
		}

		select {
		case vpnPeer.outboundCh <- packet:
			packets[i] = nil
		default:
		}
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
	localIP    atomic.Pointer[net.IP]
	inboundCh  chan *vpn.Packet // from remote peer to us
	outboundCh chan *vpn.Packet // from us to remote
}

func NewVpnPeer(peerID peer.ID, localIP net.IP) *VpnPeer {
	p := &VpnPeer{
		peerID:     peerID,
		inboundCh:  make(chan *vpn.Packet, packetHandlersChanCap),
		outboundCh: make(chan *vpn.Packet, packetHandlersChanCap),
	}

	p.localIP.Store(&localIP)

	return p
}

// TODO: remove Tunnel from VpnPeer dependencies
func (vp *VpnPeer) Start(t *Tunnel) {
	go vp.backgroundInboundHandler(t)

	for i := 0; i < t.conf.P2pNode.ParallelSendingStreamsCount; i++ {
		go vp.backgroundOutboundHandler(t)
	}
}

func (vp *VpnPeer) Close(t *Tunnel) {
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
	)
	var (
		stream                  network.Stream
		currentPacketsForStream int
	)
	sendPacket := func(packet *vpn.Packet) (err error) {
		if stream == nil {
			// TODO: increase timeout?
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			stream, err = t.makeTunnelStream(ctx, vp.peerID)
			cancel()
			if err != nil {
				return fmt.Errorf("make tunnel stream: %v", err)
			}
		}

		tmpPacket := t.device.GetTempPacket()
		defer t.device.PutTempPacket(tmpPacket)

		protocolPacket := protocol.WritePacketToBuf(tmpPacket.Buffer[:], packet.Packet)
		_, err = stream.Write(protocolPacket)

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
			if currentPacketsForStream == maxPacketsPerStream {
				closeStream()
			}
			currentPacketsForStream += 1
			// TODO: send multiple packets at once?
			err := sendPacket(packet)
			if err != nil {
				localIP := *vp.localIP.Load()
				t.logger.Warnf("send packet to peerID (%s) local ip (%s): %v", vp.peerID, localIP, err)
				closeStream()
			}
			t.device.PutTempPacket(packet)
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
		localIP := *vp.localIP.Load()
		ok := packet.Parse()
		if !ok {
			t.logger.Warnf("got invalid packet from peerID (%s) local ip (%s)", vp.peerID, localIP)
			t.device.PutTempPacket(packet)
			continue
		}
		// TODO: add batching
		err := t.device.WritePacket(packet, localIP)
		if err != nil {
			t.logger.Warnf("write packet to vpn: %v", err)
		}

		t.device.PutTempPacket(packet)
	}
}
