package service

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/vpn"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
)

const (
	packetHandlersChanCap = 200
)

type Tunnel struct {
	p2p          *P2pService
	conf         *config.Config
	device       *vpn.Device
	logger       *log.ZapEventLogger
	peersLock    sync.RWMutex
	peerIDToPeer map[peer.ID]*VpnPeer
	netIPToPeer  map[string]*VpnPeer
}

func NewTunnel(p2pService *P2pService, device *vpn.Device, conf *config.Config) *Tunnel {
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
	vpnPeer, ok := t.peerIDToPeer[peerID]
	t.peersLock.RUnlock()
	if !ok {
		t.logger.Infof("Unknown peer %s tried to tunnel packet", peerID)
		return
	}

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
		wrappedStream := io.LimitReader(stream, int64(packetSize))
		_, err = packet.ReadFrom(wrappedStream)
		if err != nil {
			t.logger.Warnf("read to packet: %v", err)
			t.device.PutTempPacket(packet)
			return
		}
		select {
		case vpnPeer.inboundCh <- packet:
		default:
			// REMOVE
			t.logger.Warnf("inbound reader dropped packet, len %d", len(packet.Packet))
			t.device.PutTempPacket(packet)
		}
	}
}

func (t *Tunnel) RefreshPeersList() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	// TODO: delete peers from maps when peer has been removed from config
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

		vpnPeer := &VpnPeer{
			peerID:     peerID,
			localIP:    localIP,
			inboundCh:  make(chan *vpn.Packet, packetHandlersChanCap),
			outboundCh: make(chan *vpn.Packet, packetHandlersChanCap),
		}
		t.peerIDToPeer[peerID] = vpnPeer
		t.netIPToPeer[string(localIP)] = vpnPeer
		go vpnPeer.backgroundInboundHandler(t)
		go vpnPeer.backgroundOutboundHandler(t)
	}
}

func (t *Tunnel) backgroundReadPackets() {
	for packet := range t.device.OutboundChan() {
		t.peersLock.RLock()
		vpnPeer, ok := t.netIPToPeer[string(packet.Dst)]
		t.peersLock.RUnlock()
		if !ok {
			t.device.PutTempPacket(packet)
			continue
		}

		select {
		case vpnPeer.outboundCh <- packet:
		default:
			t.device.PutTempPacket(packet)
		}
	}
}

func (t *Tunnel) makeTunnelStream(peerID peer.ID) (network.Stream, error) {
	// TODO: set timeout on context
	err := t.p2p.ConnectPeer(context.Background(), peerID)
	if err != nil {
		return nil, err
	}

	stream, err := t.p2p.NewStream(peerID, protocol.TunnelPacketMethod)
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
}

// TODO: remove Tunnel from VpnPeer dependencies
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
			stream, err = t.makeTunnelStream(vp.peerID)
			if err != nil {
				return err
			}
		}
		err = protocol.WriteUint64(stream, uint64(len(packet.Packet)))
		if err != nil {
			return err
		}
		_, err = stream.Write(packet.Packet)
		return err
	}

	closeStream := func() {
		if stream != nil {
			_ = stream.Close()
			stream = nil
		}
		currentPacketsForStream = 0
	}

	idleTicker := time.NewTicker(idleStreamTimeout)
	for {
		select {
		case packet := <-vp.outboundCh:
			if currentPacketsForStream == maxPacketsPerStream {
				closeStream()
			}
			currentPacketsForStream += 1
			err := sendPacket(packet)
			if err != nil {
				t.logger.Warnf("send packet to peerID (%s) local ip (%s): %v", vp.peerID, vp.localIP, err)
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

// TODO: remove Tunnel from VpnPeer dependencies
func (vp *VpnPeer) backgroundInboundHandler(t *Tunnel) {
	for packet := range vp.inboundCh {
		ok := packet.Parse()
		if !ok {
			t.logger.Warnf("got invalid packet from peerID (%s) local ip (%s)", vp.peerID, vp.localIP)
			t.device.PutTempPacket(packet)
			continue
		}
		err := t.device.WritePacket(packet, vp.localIP)
		if err != nil {
			t.logger.Warnf("write packet to vpn: %v", err)
		}

		t.device.PutTempPacket(packet)
	}
}
