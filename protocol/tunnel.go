package protocol

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/anywherelan/awl/vpn"
)

// Tunnel length-prefix layout:
//
//	bit 63    : flagGatewayForward
//	bit 62    : flagGatewayReturn
//	bits 0-61 : packet length
//
// At most one direction bit may be set; both set is a protocol error.
// Old peers never set these bits (they don't know about VPN gateway mode),
// and new peers never send flagged packets to old peers (status-exchange
// gating via CanUseAsVPNGateway() blocks it), so backward compat holds with
// no negotiation. If a flagged packet ever reaches an old reader, its
// uint64 length overflows int64 → io.LimitedReader.N < 0 → silent drop on
// parse, not corruption.
const (
	tunnelFlagGatewayForward uint64 = 1 << 63
	tunnelFlagGatewayReturn  uint64 = 1 << 62
	tunnelLengthMask         uint64 = ^(tunnelFlagGatewayForward | tunnelFlagGatewayReturn)
	// tunnelMaxLength bounds the per-packet size we'll accept on read. The
	// real cap is vpn.maxContentSize (MTU + overhead); this is a generous
	// upper bound to reject obvious garbage early without crossing package
	// dependencies.
	tunnelMaxLength uint64 = 1 << 20
)

// ReadPacketHeader reads an 8-byte tunnel packet header from the stream and
// returns the body size and the gateway direction encoded in the high bits.
// Both direction bits set is rejected as a protocol error.
func ReadPacketHeader(stream io.Reader) (size uint64, dir vpn.GatewayDir, err error) {
	var data [8]byte
	if _, err = io.ReadFull(stream, data[:]); err != nil {
		return 0, 0, err
	}
	v := binary.BigEndian.Uint64(data[:])
	fwd := v&tunnelFlagGatewayForward != 0
	ret := v&tunnelFlagGatewayReturn != 0
	if fwd && ret {
		return 0, 0, fmt.Errorf("invalid tunnel header: both gateway flags set")
	}
	switch {
	case fwd:
		dir = vpn.GatewayDirForward
	case ret:
		dir = vpn.GatewayDirReturn
	}
	size = v & tunnelLengthMask
	if size > tunnelMaxLength {
		return 0, 0, fmt.Errorf("invalid tunnel header: size %d exceeds max %d", size, tunnelMaxLength)
	}
	return size, dir, nil
}

// AppendPacketToBuf appends a length-prefixed tunnel packet to buf. The high
// bits of the length prefix encode dir; see ReadPacketHeader.
func AppendPacketToBuf(buf, packet []byte, dir vpn.GatewayDir) []byte {
	var header [8]byte
	v := uint64(len(packet))
	switch dir {
	case vpn.GatewayDirNone:
		// no flag bits
	case vpn.GatewayDirForward:
		v |= tunnelFlagGatewayForward
	case vpn.GatewayDirReturn:
		v |= tunnelFlagGatewayReturn
	}
	binary.BigEndian.PutUint64(header[:], v)
	buf = append(buf, header[:]...)
	buf = append(buf, packet...)
	return buf
}
