package service

import (
	"context"
	"net"
	"sync"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/vpn"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/helpers"
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
		go func() {
			_ = helpers.FullClose(stream)
		}()
	}()

	peerID := stream.Conn().RemotePeer()
	t.peersLock.RLock()
	vpnPeer, ok := t.peerIDToPeer[peerID]
	t.peersLock.RUnlock()
	if !ok {
		t.logger.Infof("Unknown peer %s tried to tunnel packet", peerID)
		return
	}

	packet := t.device.GetTempPacket()
	_, err := packet.ReadFrom(stream)
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
			// REMOVE
			t.logger.Warnf("outbound reader dropped packet, len %d", len(packet.Packet))
			t.device.PutTempPacket(packet)
		}
	}
}

func (t *Tunnel) sendPacket(peerID peer.ID, packet *vpn.Packet) error {
	err := t.p2p.ConnectPeer(context.Background(), peerID)
	if err != nil {
		return err
	}

	stream, err := t.p2p.NewStream(peerID, protocol.TunnelPacketMethod)
	if err != nil {
		return err
	}
	defer func() {
		go func() {
			_ = helpers.FullClose(stream)
		}()
	}()

	_, err = stream.Write(packet.Packet)
	if err != nil {
		return err
	}

	return nil
}

type VpnPeer struct {
	peerID     peer.ID
	localIP    net.IP
	inboundCh  chan *vpn.Packet
	outboundCh chan *vpn.Packet // from us to remote
}

// TODO: remove Tunnel from VpnPeer dependencies
func (vp *VpnPeer) backgroundOutboundHandler(t *Tunnel) {
	for packet := range vp.outboundCh {
		err := t.sendPacket(vp.peerID, packet)
		if err != nil {
			t.logger.Warnf("send packet to peerID (%s) local ip (%s): %v", vp.peerID, vp.localIP, err)
		}
		t.device.PutTempPacket(packet)
	}
}

// TODO: remove Tunnel from VpnPeer dependencies
func (vp *VpnPeer) backgroundInboundHandler(t *Tunnel) {
	for packet := range vp.inboundCh {
		packet.Parse()
		err := t.device.WritePacket(packet, vp.localIP)
		if err != nil {
			t.logger.Warnf("write packet to vpn: %v", err)
		}

		t.device.PutTempPacket(packet)
	}
}
