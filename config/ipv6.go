package config

import (
	"crypto/sha256"
	"net"

	"github.com/libp2p/go-libp2p/core/peer"
)

// deriveIPv6FromPeerID deterministically maps a libp2p peer ID to its overlay
// IPv6 address inside the given awl IPv6 subnet prefix. The result is a stable,
// network-wide identity: every node derives the same address for a given peer —
// no allocation, no coordination, no conflict checks.
//
//	addr = prefix || SHA-256(rawPeerID)[...]   // fills remaining host bits
//
// rawPeerID is the *binary* multihash ([]byte(id)). For ed25519 keys, this provides
// a stable entropy source. Truncating SHA-256 is a standard construction, making
// the host bits collision-safe.
func DeriveIPv6FromPeerID(id peer.ID, prefix *net.IPNet) net.IP {
	if prefix == nil || len(prefix.Mask) != net.IPv6len {
		return nil
	}

	sum := sha256.Sum256([]byte(id))

	// Get the prefix network address as a 16-byte array.
	base := prefix.IP.Mask(prefix.Mask).To16()
	if base == nil {
		return nil
	}

	addr := make(net.IP, net.IPv6len)
	copy(addr, base)

	// Dynamically embed the hash into the host portion of the address using the inverted mask.
	// This works flawlessly for ANY valid prefix length (e.g. /48, /64, /120, etc.)
	for i := 0; i < net.IPv6len; i++ {
		addr[i] |= sum[i] & ^prefix.Mask[i]
	}

	// Never hand out the all-zero host (Subnet-Router Anycast, RFC 4291 §2.6.1).
	allZero := true
	for i := 0; i < net.IPv6len; i++ {
		if (addr[i] & ^prefix.Mask[i]) != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		addr[15] |= 1
	}

	return addr
}
