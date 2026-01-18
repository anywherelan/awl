package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGenerateNextIpAddrExcept(t *testing.T) {
	// Setup base config with a VPN network
	conf := &Config{
		VPNConfig: VPNConfig{
			IPNet: defaultNetworkSubnet,
		},
		KnownPeers: map[string]KnownPeer{},
	}
	// Helper to add a known peer
	addPeer := func(ip string) {
		conf.KnownPeers[ip] = KnownPeer{
			PeerID:    "peer-" + ip,
			IPAddr:    ip,
			CreatedAt: time.Now(),
		}
	}

	t.Run("NoPeers_NoExceptions", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		ip := conf.GenerateNextIpAddrExcept(nil)
		assert.Equal(t, "10.66.0.2", ip) // First IP after network address
	})

	t.Run("WithPeers_NoExceptions", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.2")
		addPeer("10.66.0.5")

		ip := conf.GenerateNextIpAddrExcept(nil)
		assert.Equal(t, "10.66.0.6", ip) // Next after max known (5) -> 6
	})

	t.Run("ExceptionScanningNextIP", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.10")

		// Next should be .11, but we exclude it
		exceptions := []string{"10.66.0.11"}
		ip := conf.GenerateNextIpAddrExcept(exceptions)
		assert.Equal(t, "10.66.0.12", ip)
	})

	t.Run("MultipleExceptionsSequential", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.10")

		// Next .11, .12, .13 are excluded
		exceptions := []string{"10.66.0.11", "10.66.0.12", "10.66.0.13"}
		ip := conf.GenerateNextIpAddrExcept(exceptions)
		assert.Equal(t, "10.66.0.14", ip)
	})

	t.Run("ExceptionsOutOfOrder", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.10")

		// .12 and .11 are excluded (order shouldn't matter)
		exceptions := []string{"10.66.0.12", "10.66.0.11"}
		ip := conf.GenerateNextIpAddrExcept(exceptions)
		assert.Equal(t, "10.66.0.13", ip)
	})

	t.Run("NonConflictingExceptions", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.10")

		// Exception is lower than current max, should be ignored
		exceptions := []string{"10.66.0.5"}
		ip := conf.GenerateNextIpAddrExcept(exceptions)
		assert.Equal(t, "10.66.0.11", ip)
	})

	t.Run("ExceptionIsCurrentMax", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.10")

		exceptions := []string{"10.66.0.10"}
		ip := conf.GenerateNextIpAddrExcept(exceptions)
		assert.Equal(t, "10.66.0.11", ip)
	})

	t.Run("ComplexScenario", func(t *testing.T) {
		conf.KnownPeers = map[string]KnownPeer{}
		addPeer("10.66.0.10")
		addPeer("10.66.0.20")

		// Max is 20. Next candidate is 21.
		// Exclude 21, 23.
		// 21 -> excluded -> try 22.
		// 22 -> ok.
		exceptions := []string{"10.66.0.21", "10.66.0.23"}
		ip := conf.GenerateNextIpAddrExcept(exceptions)
		assert.Equal(t, "10.66.0.22", ip)
	})
}

func TestCheckIPUnique(t *testing.T) {
	conf := &Config{
		VPNConfig: VPNConfig{
			IPNet: "10.66.0.0/24",
		},
		KnownPeers: map[string]KnownPeer{
			"p1": {PeerID: "p1", IPAddr: "10.66.0.1", Alias: "peer1"},
			"p2": {PeerID: "p2", IPAddr: "10.66.0.2", Alias: "peer2"},
		},
	}

	tests := []struct {
		name         string
		checkIP      string
		exceptPeerID string
		wantErr      string
	}{
		{"ValidNewIP", "10.66.0.3", "", ""},
		{"ExistingIP", "10.66.0.1", "", "ip 10.66.0.1 is already used by peer peer1"},
		{"SamePeerIP", "10.66.0.1", "p1", ""},
		{"InvalidIPFormat", "invalid", "", "invalid IP invalid"},
		{"OutsideSubnet", "192.168.1.1", "", "IP 192.168.1.1 does not belong to subnet 10.66.0.0/24"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conf.CheckIPUnique(tt.checkIP, tt.exceptPeerID)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}
