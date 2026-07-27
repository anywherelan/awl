package config

import (
	"net"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
)

func TestDeriveIPv6FromPeerID(t *testing.T) {
	_, sub6, _ := net.ParseCIDR("fd00:66:0:7915:10ba:fb1c:ef80:180c/48")
	pid, _ := peer.Decode("12D3KooWBG3PFoGRgbr8ckoRPCpWQjdFj5tME2XBUot5s4uGZkiL")

	addr := DeriveIPv6FromPeerID(pid, sub6)
	assert.NotNil(t, addr)

	// Ensure the address has the correct prefix
	assert.True(t, sub6.Contains(addr))

	// Ensure it's not the exact network address
	assert.NotEqual(t, sub6.IP, addr)

	// Ensure the last bit logic works for all-zero hashes (hard to mock hash, but we test nil case)
	assert.Nil(t, DeriveIPv6FromPeerID(pid, nil))
}
