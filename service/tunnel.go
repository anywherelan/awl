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

	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/metrics"
	"github.com/anywherelan/awl/protocol"
	"github.com/anywherelan/awl/vpn"
)

const (
	// approx 6.6 MiB
	packetHandlersChanCap = 2000
)

type Tunnel struct {
	p2p    P2p
	conf   *config.Config
	device *vpn.Device
	logger *log.ZapEventLogger

	isClosed     atomic.Bool
	peersLock    sync.RWMutex
	peerIDToPeer map[peer.ID]*VpnPeer
	// netIPToPeer maps both IPv4 and IPv6 string representations to a VpnPeer.
	netIPToPeer      map[string]*VpnPeer
	udpBroadcastAddr net.IP

	// VPN gateway mode fields (protected by peersLock).
	vpnGatewayClientEnabled bool     // client side: we're using a gateway
	vpnGatewayPeerID        peer.ID  // client side: which peer is our gateway
	vpnGatewayPeer          *VpnPeer // resolved VpnPeer for outbound gateway traffic; rebound on RefreshPeersList
	vpnGatewayServerEnabled bool     // server side: we serve as a VPN gateway for others
	// awlSubnet is set once in NewTunnel and never mutated afterwards.
	awlSubnet  *net.IPNet
	awlSubnet6 *net.IPNet

	// gatewayConnEmitter emits awlevent.VPNGatewayConnectivityChanged. May be
	// nil if the event bus had no emitter. vpnGatewayConnected holds the last
	// emitted connectivity state so onPeerConnected/onPeerDisconnected only fire
	// on real transitions (they run on every libp2p per-stream event).
	gatewayConnEmitter  awlevent.Emitter
	vpnGatewayConnected atomic.Bool

	// dnsHandler, when set, receives packets addressed to the in-tunnel DNS IP
	// (the Android DNS interceptor). atomic.Pointer because the Tunnel is
	// created before the DNS service that installs the handler.
	dnsHandler atomic.Pointer[dnsHandler]
}

// DNSPacketHandler receives IP packets addressed to the in-tunnel DNS IP.
// HandlePacket must copy the packet data: the caller reuses the buffer.
type DNSPacketHandler interface {
	HandlePacket(packet []byte)
}

type dnsHandler struct {
	ip      net.IP
	handler DNSPacketHandler
}

func NewTunnel(p2pService P2p, device *vpn.Device, conf *config.Config, eventbus awlevent.Bus) *Tunnel {
	localIP, netMask := conf.VPNLocalIPMask()
	awlSubnet := &net.IPNet{IP: localIP, Mask: netMask}
	udpBroadcastAddr := vpn.GetIPv4BroadcastAddress(awlSubnet)

	var awlSubnet6 *net.IPNet
	localIPV6, netMaskV6 := conf.VPNLocalIPMaskV6()
	if localIPV6 != nil {
		awlSubnet6 = &net.IPNet{IP: localIPV6, Mask: netMaskV6}
	}

	emitter, err := eventbus.Emitter(new(awlevent.VPNGatewayConnectivityChanged))
	if err != nil {
		panic(err)
	}

	tunnel := &Tunnel{
		p2p:                     p2pService,
		conf:                    conf,
		device:                  device,
		logger:                  log.Logger("awl/service/tunnel"),
		peerIDToPeer:            make(map[peer.ID]*VpnPeer),
		netIPToPeer:             make(map[string]*VpnPeer),
		udpBroadcastAddr:        udpBroadcastAddr,
		vpnGatewayServerEnabled: conf.VPNGateway.ServerEnabled,
		awlSubnet:               awlSubnet,
		awlSubnet6:              awlSubnet6,
		gatewayConnEmitter:      emitter,
	}
	tunnel.RefreshPeersList()
	p2pService.SubscribeConnectionEvents(tunnel.onPeerConnected, tunnel.onPeerDisconnected)

	return tunnel
}

// SetDNSHandler installs h as the receiver of packets addressed to dnsIP.
// Safe to call concurrently with HandleReadPackets.
func (t *Tunnel) SetDNSHandler(dnsIP net.IP, h DNSPacketHandler) {
	t.dnsHandler.Store(&dnsHandler{ip: dnsIP, handler: h})
}

func (t *Tunnel) StreamHandler(stream network.Stream) {
	peerID := stream.Conn().RemotePeer()

	defer func() {
		if r := recover(); r != nil {
			// This typically happens if vpnPeer.inboundCh is closed concurrently
			// during a tunnel restart or peer removal.
			t.logger.Debugf("StreamHandler recovered from panic (likely channel closed) for peer %s: %v", peerID, r)
		}
		_ = stream.Close()
	}()

	t.peersLock.RLock()
	vpnPeer, ok := t.peerIDToPeer[peerID]
	t.peersLock.RUnlock()
	if !ok {
		t.logger.Infof("Unknown peer %s tried to tunnel packet", peerID)
		return
	}

	wrappedStream := &io.LimitedReader{}
	for {
		packet := t.device.GetTempPacket()
		packetSize, dir, err := protocol.ReadPacketHeader(stream)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.logger.Warnf("read packet header: %v", err)
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
		packet.GatewayDir = dir

		select {
		case vpnPeer.inboundCh <- packet:
		default:
			metrics.VPNPacketsDroppedTotal.WithLabelValues("inbound_channel_full").Inc()
			t.logger.Warnf("inbound reader dropped packet for peer %s", peerID)
			t.device.PutTempPacket(packet)
		}
	}
}

func (t *Tunnel) RefreshPeersList() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	if t.isClosed.Load() {
		return
	}

	t.conf.RLock()
	defer t.conf.RUnlock()
	for _, knownPeer := range t.conf.KnownPeers {
		peerID := knownPeer.PeerId()
		newLocalIP := net.ParseIP(knownPeer.IPAddr).To4()
		if newLocalIP == nil {
			t.logger.Errorf("Known peer %q has invalid IP %s in conf", knownPeer.DisplayName(), knownPeer.IPAddr)
			continue
		}
		newLocalIPv6 := net.ParseIP(knownPeer.IPAddrV6)

		prevPeer, exists := t.peerIDToPeer[peerID]
		if !exists {
			// add new peer
			vpnPeer := NewVpnPeer(peerID, newLocalIP, newLocalIPv6)
			t.peerIDToPeer[peerID] = vpnPeer
			t.netIPToPeer[newLocalIP.String()] = vpnPeer
			if newLocalIPv6 != nil {
				t.netIPToPeer[newLocalIPv6.String()] = vpnPeer
				t.logger.Debugf("mapping peer %s (%s) to IPv6 %s", peerID, newLocalIP, newLocalIPv6)
			}
			vpnPeer.Start(t)
			continue
		}

		oldLocalIP := *prevPeer.localIP.Load()
		var oldLocalIPv6 net.IP
		if p := prevPeer.localIPv6.Load(); p != nil {
			oldLocalIPv6 = *p
		}

		ipChanged := !oldLocalIP.Equal(newLocalIP)
		ipv6Changed := !oldLocalIPv6.Equal(newLocalIPv6)

		if !ipChanged && !ipv6Changed {
			// no changes
			continue
		}

		// IP changed: update both IPv4 and IPv6 mappings
		if ipChanged {
			delete(t.netIPToPeer, oldLocalIP.String())
			prevPeer.localIP.Store(&newLocalIP)
			t.netIPToPeer[newLocalIP.String()] = prevPeer
		}

		if ipv6Changed {
			if oldLocalIPv6 != nil {
				delete(t.netIPToPeer, oldLocalIPv6.String())
			}
			if newLocalIPv6 != nil {
				prevPeer.localIPv6.Store(&newLocalIPv6)
				t.netIPToPeer[newLocalIPv6.String()] = prevPeer
			} else {
				prevPeer.localIPv6.Store(nil)
			}
		}
	}

	// delete unknown peers
	for _, vpnPeer := range t.peerIDToPeer {
		_, exists := t.conf.KnownPeers[vpnPeer.peerID.String()]
		if exists {
			continue
		}
		localIP := *vpnPeer.localIP.Load()
		var localIPv6 net.IP
		if p := vpnPeer.localIPv6.Load(); p != nil {
			localIPv6 = *p
		}
		vpnPeer.Close(t)
		delete(t.peerIDToPeer, vpnPeer.peerID)
		delete(t.netIPToPeer, localIP.String())
		if localIPv6 != nil {
			delete(t.netIPToPeer, localIPv6.String())
		}
	}

	// Rebind gateway pointer to the (possibly new) VpnPeer for the configured gateway peer.
	if t.vpnGatewayClientEnabled {
		gwPeer, ok := t.peerIDToPeer[t.vpnGatewayPeerID]
		if !ok {
			t.logger.Warnf("VPN gateway peer %s no longer in KnownPeers, disabling gateway client mode", t.vpnGatewayPeerID)
			t.vpnGatewayClientEnabled = false
			t.vpnGatewayPeerID = ""
			t.vpnGatewayPeer = nil
			t.vpnGatewayConnected.Store(false)
		} else {
			t.vpnGatewayPeer = gwPeer
		}
	}

	// Recompute isGatewayClient for every peer. WeAllowUsingAsExitNode may
	// have changed for any peer (peer settings update path)
	for _, kp := range t.conf.KnownPeers {
		vp, ok := t.peerIDToPeer[kp.PeerId()]
		if !ok {
			continue
		}
		vp.weAllowUsingAsExitNode.Store(kp.WeAllowUsingAsExitNode)
	}
}

func (t *Tunnel) Close() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	t.isClosed.Store(true)

	for _, vpnPeer := range t.peerIDToPeer {
		localIP := *vpnPeer.localIP.Load()
		var localIPv6 net.IP
		if p := vpnPeer.localIPv6.Load(); p != nil {
			localIPv6 = *p
		}
		vpnPeer.Close(t)
		delete(t.peerIDToPeer, vpnPeer.peerID)
		delete(t.netIPToPeer, localIP.String())
		if localIPv6 != nil {
			delete(t.netIPToPeer, localIPv6.String())
		}
	}
}

// HandleReadPackets for successfully handled packets it sets packet in slice as nil
func (t *Tunnel) HandleReadPackets(packets []*vpn.Packet) {
	t.peersLock.RLock()
	defer t.peersLock.RUnlock()

	if t.isClosed.Load() {
		return
	}
	dnsIntercept := t.dnsHandler.Load()

	for i, packet := range packets {
		if packet == nil {
			continue
		}

		// DNS queries to the in-tunnel DNS IP (Android interceptor). Must come
		// before the broadcast and gateway branches.
		if dnsIntercept != nil && packet.Dst.Equal(dnsIntercept.ip) {
			dnsIntercept.handler.HandlePacket(packet.Packet)
			continue
		}

		// P2P broadcast/unicast lookup
		vpnPeer, isP2P := t.netIPToPeer[packet.Dst.String()]
		if isP2P {
			// VPN gateway server: tag NAT-returned packets so the client peer
			// applies a dst-only rewrite on receive. Discriminator: peer is
			// our gateway client AND src is outside our awl subnet (i.e. came
			// from the internet via NAT, not our own p2p initiative to the
			// same peer). Subnet check is local to this side — no cross-side
			// dependency on the client's awl subnet.
			srcFromInternet := !t.isAWLSubnet(packet.Src, packet.IsIPv6)

			if vpnPeer.weAllowUsingAsExitNode.Load() && t.vpnGatewayServerEnabled && srcFromInternet {
				packet.GatewayDir = vpn.GatewayDirReturn
			}
			select {
			case vpnPeer.outboundCh <- packet:
				packets[i] = nil
			default:
				metrics.VPNPacketsDroppedTotal.WithLabelValues("outbound_channel_full").Inc()
			}
			continue
		}

		// IPv4 broadcast
		if !packet.IsIPv6 && (packet.Dst.Equal(t.udpBroadcastAddr) || packet.Dst.Equal(net.IPv4bcast)) {
			for _, vpnPeer := range t.peerIDToPeer {
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

		// VPN gateway client mode: forward non-local packets to the gateway peer.
		if t.vpnGatewayClientEnabled && t.vpnGatewayPeer != nil {
			isAWLSubnet := t.isAWLSubnet(packet.Dst, packet.IsIPv6)

			if isNonRoutableIP(packet.Dst) || isAWLSubnet {
				continue
			}
			packet.GatewayDir = vpn.GatewayDirForward
			select {
			case t.vpnGatewayPeer.outboundCh <- packet:
				packets[i] = nil
			default:
				metrics.VPNPacketsDroppedTotal.WithLabelValues("gateway_channel_full").Inc()
			}
			continue
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

func (t *Tunnel) isAWLSubnet(ip net.IP, isIPv6 bool) bool {
	if isIPv6 {
		if t.awlSubnet6 != nil {
			return t.awlSubnet6.Contains(ip)
		}
		return false
	}
	return t.awlSubnet.Contains(ip)
}

type VpnPeer struct {
	peerID                 peer.ID
	localIP                atomic.Pointer[net.IP]
	localIPv6              atomic.Pointer[net.IP]
	weAllowUsingAsExitNode atomic.Bool

	inboundCh  chan *vpn.Packet // from remote peer to us
	outboundCh chan *vpn.Packet // from us to remote

	ctx       context.Context
	ctxCancel context.CancelFunc
}

func NewVpnPeer(peerID peer.ID, localIP net.IP, localIPv6 net.IP) *VpnPeer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &VpnPeer{
		peerID:     peerID,
		inboundCh:  make(chan *vpn.Packet, packetHandlersChanCap),
		outboundCh: make(chan *vpn.Packet, packetHandlersChanCap),
		ctx:        ctx,
		ctxCancel:  cancel,
	}

	p.localIP.Store(&localIP)
	if localIPv6 != nil {
		p.localIPv6.Store(&localIPv6)
	}

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
		// 5 GiB. Idk why, just in case
		maxPacketsPerUnlimitedStream = 5 << 30 / vpn.InterfaceMTU
		// 20 MiB. The same limit is set in awl-bootstrap-node
		maxPacketsPerLimitedStream = 20 << 20 / vpn.InterfaceMTU
		idleStreamTimeout          = 30 * time.Second
		// approx 340 KiB
		packetsBatchSize = 100
	)
	var (
		stream                  network.Stream
		maxPacketsPerStream     int
		currentPacketsForStream int
		bytesBuf                []byte
		packetsBuf              = make([]*vpn.Packet, packetsBatchSize)
	)

	sendPacket := func(packets []*vpn.Packet) (err error) {
		if stream == nil {
			ctx, cancel := context.WithTimeout(vp.ctx, 2*time.Second)
			stream, err = t.makeTunnelStream(ctx, vp.peerID)
			cancel()
			if err != nil {
				metrics.VPNStreamOpenErrorsTotal.Inc()
				return fmt.Errorf("make tunnel stream: %v", err)
			}
			if stream.Stat().Limited {
				maxPacketsPerStream = maxPacketsPerLimitedStream
			} else {
				maxPacketsPerStream = maxPacketsPerUnlimitedStream
			}

			bytesBuf = make([]byte, 0, packetsBatchSize*(vpn.InterfaceMTU+8))
		}

		data := bytesBuf[:0]
		for _, packet := range packets {
			data = protocol.AppendPacketToBuf(data, packet.Packet, packet.GatewayDir)
		}
		_, err = stream.Write(data)
		dataLen := len(data)
		bytesBuf = data[:0]

		if err == nil {
			metrics.VPNPacketsSentTotal.Add(float64(len(packets)))
			metrics.VPNBytesSentTotal.Add(float64(dataLen))
		}

		return err
	}

	closeStream := func() {
		if stream != nil {
			_ = stream.Close()
			stream = nil
		}
		currentPacketsForStream = 0
		// free buffer when idle
		bytesBuf = nil
	}

	clearTempPackets := func(packets []*vpn.Packet) {
		for i := 0; i < len(packets); i++ {
			t.device.PutTempPacket(packets[i])
			packets[i] = nil
		}
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

			packetsBuf[0] = packet
			packetsBatch := readBatchFromChan(vp.outboundCh, packetsBuf, 1)

			if !t.p2p.IsConnected(vp.peerID) {
				// we should be connected beforehand, e.g. in p2p.MaintainBackgroundConnections
				clearTempPackets(packetsBatch)
				continue
			}

			if currentPacketsForStream+len(packetsBatch) >= maxPacketsPerStream {
				closeStream()
			}

			currentPacketsForStream += len(packetsBatch)
			err := sendPacket(packetsBatch)
			if err != nil {
				localIP := *vp.localIP.Load()
				t.logger.Warnf("failed to send %d packets to peerID (%s) local ip (%s): %v", len(packetsBatch), vp.peerID, localIP, err)
				closeStream()
			}

			clearTempPackets(packetsBatch)
		case <-idleTicker.C:
			if len(vp.outboundCh) == 0 {
				closeStream()
			}
		}
	}
}

func (vp *VpnPeer) backgroundInboundHandler(t *Tunnel) {
	batchSize := t.device.BatchSize()
	bytesBufs := make([][]byte, 0, batchSize)
	packetsBufs := make([]*vpn.Packet, batchSize)

	for {
		firstPacket, open := <-vp.inboundCh
		if !open {
			return
		}
		localIP := *vp.localIP.Load()

		packetsBufs[0] = firstPacket
		packetsBatch := readBatchFromChan(vp.inboundCh, packetsBufs, 1)

		newLen := 0
		for i, packet := range packetsBatch {
			ok := packet.Parse()
			if !ok {
				metrics.VPNPacketsDroppedTotal.WithLabelValues("parse_error").Inc()
				t.logger.Warnf("got invalid packet from peerID (%s) local ip (%s)", vp.peerID, localIP)
				t.device.PutTempPacket(packet)
				packetsBatch[i] = nil
				continue
			}
			packetsBatch[newLen] = packet
			newLen++
		}
		filteredPackets := packetsBatch[:newLen]

		if len(filteredPackets) > 0 {
			err := t.writeInboundBatch(filteredPackets, bytesBufs, localIP, vp)
			if err != nil {
				t.logger.Warnf("write packets batch to vpn for local ip %s: %v", localIP, err)
			} else {
				metrics.VPNPacketsReceivedTotal.Add(float64(len(filteredPackets)))
				packetsLen := 0
				for _, p := range filteredPackets {
					packetsLen += len(p.Packet)
				}
				metrics.VPNBytesReceivedTotal.Add(float64(packetsLen))
			}
		}

		for i, packet := range filteredPackets {
			if packet == nil {
				continue
			}
			t.device.PutTempPacket(packet)
			filteredPackets[i] = nil
		}
	}
}

// SetVPNGatewayServerEnabled enables or disables VPN gateway server mode on
// this tunnel and persists the choice in the config. The decision propagates
// to other peers on the next status exchange so their UI reflects whether
// this node is currently offering VPN gateway server.
func (t *Tunnel) SetVPNGatewayServerEnabled(enabled bool) {
	t.peersLock.Lock()
	t.vpnGatewayServerEnabled = enabled
	t.conf.Lock()
	t.conf.VPNGateway.ServerEnabled = enabled
	t.conf.Save()
	t.conf.Unlock()
	t.peersLock.Unlock()
}

// SetVPNGatewayPeer enables VPN gateway client mode using the existing VpnPeer
// for the given gateway peer, validates the peer's permission, and persists
// the choice in the config.
//
// The peer must:
//  1. Be in KnownPeers.
//  2. Currently allow being used as a VPN gateway (KnownPeer.CanUseAsVPNGateway,
//     i.e. AllowedUsingAsExitNode AND RemoteVPNGatewayServerEnabled from the
//     most recent status exchange).
func (t *Tunnel) SetVPNGatewayPeer(gatewayPeerID peer.ID) error {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	knownPeer, ok := t.conf.GetPeer(gatewayPeerID.String())
	if !ok {
		return fmt.Errorf("peer %s is not in known peers", gatewayPeerID)
	}
	if !knownPeer.CanUseAsVPNGateway() {
		return fmt.Errorf("peer %s is not currently a valid VPN gateway "+
			"(AllowedUsingAsExitNode=%v, RemoteVPNGatewayServerEnabled=%v)",
			gatewayPeerID, knownPeer.AllowedUsingAsExitNode, knownPeer.RemoteVPNGatewayServerEnabled)
	}

	gwPeer, ok := t.peerIDToPeer[gatewayPeerID]
	if !ok {
		return fmt.Errorf("peer %s not found in peerIDToPeer", gatewayPeerID)
	}

	t.vpnGatewayClientEnabled = true
	t.vpnGatewayPeerID = gatewayPeerID
	t.vpnGatewayPeer = gwPeer

	t.conf.Lock()
	t.conf.VPNGateway.ClientEnabled = true
	t.conf.VPNGateway.GatewayPeerID = gatewayPeerID.String()
	t.conf.Save()
	t.conf.Unlock()

	// Seed connectivity from the current state and emit it, so the UI gets an
	// immediate signal for the common case where the peer is not connected
	// when the gateway is enabled.
	connected := t.p2p.IsConnected(gatewayPeerID)
	t.vpnGatewayConnected.Store(connected)
	if !connected {
		t.emitGatewayConnectivity(connected, gatewayPeerID)
	}

	return nil
}

// ClearVPNGatewayPeer disables VPN gateway client mode and persists the choice.
func (t *Tunnel) ClearVPNGatewayPeer() {
	t.peersLock.Lock()
	defer t.peersLock.Unlock()

	t.vpnGatewayClientEnabled = false
	t.vpnGatewayPeerID = ""
	t.vpnGatewayPeer = nil
	t.vpnGatewayConnected.Store(false)

	t.conf.Lock()
	t.conf.VPNGateway.ClientEnabled = false
	t.conf.VPNGateway.GatewayPeerID = ""
	t.conf.Save()
	t.conf.Unlock()
}

func (t *Tunnel) onPeerConnected(_ network.Network, conn network.Conn) {
	t.peersLock.RLock()
	enabled := t.vpnGatewayClientEnabled
	gatewayPeerID := t.vpnGatewayPeerID
	t.peersLock.RUnlock()
	if !enabled || gatewayPeerID == "" || conn.RemotePeer() != gatewayPeerID {
		return
	}
	// libp2p fires this per connection; dedup so we emit only on the
	// disconnected -> connected transition.
	if t.vpnGatewayConnected.CompareAndSwap(false, true) {
		t.logger.Infof("VPN gateway peer %s connected", gatewayPeerID)
		t.emitGatewayConnectivity(true, gatewayPeerID)
	}
}

func (t *Tunnel) onPeerDisconnected(_ network.Network, conn network.Conn) {
	t.peersLock.RLock()
	enabled := t.vpnGatewayClientEnabled
	gatewayPeerID := t.vpnGatewayPeerID
	t.peersLock.RUnlock()
	if !enabled || gatewayPeerID == "" || conn.RemotePeer() != gatewayPeerID {
		return
	}
	// libp2p emits a disconnect per stream — this fires whenever the last
	// connection to the peer goes away. Re-check IsConnected to suppress
	// noise from transient stream closes that leave another conn alive.
	if t.p2p.IsConnected(gatewayPeerID) {
		return
	}
	if t.vpnGatewayConnected.CompareAndSwap(true, false) {
		t.logger.Warnf("VPN gateway peer %s disconnected", gatewayPeerID)
		t.emitGatewayConnectivity(false, gatewayPeerID)
	}
}

// emitGatewayConnectivity publishes a VPNGatewayConnectivityChanged edge event.
// Best-effort: emit errors are non-fatal (GatewayInfo polling remains the
// canonical source of truth).
func (t *Tunnel) emitGatewayConnectivity(connected bool, gatewayPeerID peer.ID) {
	if t.gatewayConnEmitter == nil {
		return
	}
	_ = t.gatewayConnEmitter.Emit(awlevent.VPNGatewayConnectivityChanged{
		Connected: connected,
		PeerID:    gatewayPeerID.String(),
	})
}

// writeInboundBatch rewrites IP headers per packet and writes the whole batch
// to TUN in a single syscall. Per-packet behavior is selected by the wire-level
// GatewayDir tag, which the sender stamped into the length prefix:
//
//   - GatewayDirForward (sender = gateway client → us): we must be acting as a
//     VPN gateway server AND have granted this peer exit-node permission. src
//     is rewritten to senderIP (the client's awl IP); dst is preserved so the
//     kernel can NAT-forward to the internet. Forward packets without role or
//     permission are dropped with a labelled metric.
//
//   - GatewayDirReturn (sender = our gateway server → us): we must be in
//     gateway client mode AND the sender must be our configured gateway peer.
//     dst is rewritten to localIP; src is preserved so apps see the real
//     internet source. Returns from a non-gateway peer are dropped with a
//     labelled metric.
//
//   - GatewayDirNone: normal awl peer-to-peer — full src/dst rewrite.
//
// awl subnet inspection is intentionally absent here. The on-wire tag carries
// the sender's intent explicitly, so this side does not need to re-derive it
// from packet IPs and is not exposed to a subnet mismatch between peers.
func (t *Tunnel) writeInboundBatch(packets []*vpn.Packet, bufs [][]byte, senderIP net.IP, vp *VpnPeer) error {
	t.peersLock.RLock()
	serverEnabled := t.vpnGatewayServerEnabled
	isOurGateway := t.vpnGatewayClientEnabled && vp.peerID == t.vpnGatewayPeerID
	t.peersLock.RUnlock()

	localIPv4 := t.awlSubnet.IP
	var localIPv6 net.IP
	if t.awlSubnet6 != nil {
		localIPv6 = t.awlSubnet6.IP
	}

	allowGateway := vp.weAllowUsingAsExitNode.Load()

	for _, packet := range packets {
		var localIP net.IP
		var senderIPv6 net.IP
		if packet.IsIPv6 {
			localIP = localIPv6
			if p := vp.localIPv6.Load(); p != nil {
				senderIPv6 = *p
			}
		} else {
			localIP = localIPv4
		}
		if localIP == nil {
			continue // No local IP for this family
		}

		switch packet.GatewayDir {
		case vpn.GatewayDirForward:
			if !serverEnabled {
				metrics.VPNPacketsDroppedTotal.WithLabelValues("gateway_server_disabled").Inc()
				continue
			}
			if !allowGateway {
				metrics.VPNPacketsDroppedTotal.WithLabelValues("gateway_not_allowed").Inc()
				continue
			}
			if packet.IsIPv6 {
				if senderIPv6 == nil {
					continue
				}
				copy(packet.Src, senderIPv6)
			} else {
				copy(packet.Src, senderIP)
			}
			// dst preserved (internet destination)
		case vpn.GatewayDirReturn:
			if !isOurGateway {
				metrics.VPNPacketsDroppedTotal.WithLabelValues("gateway_return_from_non_gateway").Inc()
				continue
			}
			copy(packet.Dst, localIP)
			// src preserved
		default: // P2P
			if packet.IsIPv6 {
				if senderIPv6 == nil {
					continue
				}
				copy(packet.Src, senderIPv6)
			} else {
				copy(packet.Src, senderIP)
			}
			copy(packet.Dst, localIP)
		}
		packet.RecalculateChecksum()
		bufs = append(bufs, packet.Buf())
	}

	return t.device.WriteBufs(bufs)
}

// isNonRoutableIP returns true for IPs that should not be forwarded through the gateway.
//
// TODO(gateway): add client-side drop of
// private destinations (10/8, 172.16/12, 192.168/16, CGNAT, link-local)
// before sending to the gateway: fast local refusal instead of a silent drop
// at the exit node's filter. Not a replacement for the server-side filtering
// (iptables on Linux, WFP on Windows) — the server cannot trust clients.
func isNonRoutableIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
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
