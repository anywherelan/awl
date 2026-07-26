package config

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGenerateNextIpAddrExcept(t *testing.T) {
	// Setup base config with a VPN network
	conf := &Config{
		VPNConfig: VPNConfig{
			IPNet: DefaultVPNNetworkSubnet,
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

	t.Run("SkipsNetstackDNSIP", func(t *testing.T) {
		conf := &Config{
			VPNConfig: VPNConfig{IPNet: DefaultVPNNetworkSubnet},
			KnownPeers: map[string]KnownPeer{
				"p": {PeerID: "p", IPAddr: "10.66.0.10"},
			},
		}
		conf.netstackDNSIP = net.ParseIP("10.66.0.11").To4()

		ip := conf.GenerateNextIpAddrExcept(nil)
		assert.Equal(t, "10.66.0.12", ip)
	})

	t.Run("SkipsNetstackDNSIPAndBroadcast", func(t *testing.T) {
		// 10.66.0.1/29: hosts .1 (local) — .6, broadcast .7, DNS IP .6.
		conf := &Config{
			VPNConfig: VPNConfig{IPNet: "10.66.0.1/29"},
			KnownPeers: map[string]KnownPeer{
				"p": {PeerID: "p", IPAddr: "10.66.0.5"},
			},
		}
		conf.netstackDNSIP = conf.computeNetstackDNSIP()

		// Next after max (.5) would be .6 (DNS IP, skipped) and .7 (broadcast,
		// skipped). Walking past the end of an exhausted subnet is pre-existing
		// behavior of this function, not introduced by the reserved addresses.
		ip := conf.GenerateNextIpAddrExcept(nil)
		assert.Equal(t, "10.66.0.8", ip)
	})
}

func TestDNSServerIP(t *testing.T) {
	makeConf := func(ipNet string, peerIPs ...string) *Config {
		conf := &Config{
			VPNConfig:  VPNConfig{IPNet: ipNet},
			KnownPeers: map[string]KnownPeer{},
		}
		for _, ip := range peerIPs {
			conf.KnownPeers[ip] = KnownPeer{PeerID: "peer-" + ip, IPAddr: ip}
		}
		return conf
	}

	t.Run("DefaultSubnet", func(t *testing.T) {
		conf := makeConf(DefaultVPNNetworkSubnet)
		assert.Equal(t, "10.66.255.254", conf.computeNetstackDNSIP().String())
	})

	t.Run("CandidateTakenByPeer", func(t *testing.T) {
		conf := makeConf(DefaultVPNNetworkSubnet, "10.66.255.254")
		assert.Equal(t, "10.66.255.253", conf.computeNetstackDNSIP().String())
	})

	t.Run("SeveralCandidatesTakenByPeers", func(t *testing.T) {
		conf := makeConf(DefaultVPNNetworkSubnet, "10.66.255.254", "10.66.255.253")
		assert.Equal(t, "10.66.255.252", conf.computeNetstackDNSIP().String())
	})

	t.Run("CandidateTakenByOwnIP", func(t *testing.T) {
		conf := makeConf("10.66.255.254/16")
		assert.Equal(t, "10.66.255.253", conf.computeNetstackDNSIP().String())
	})

	t.Run("ExhaustedSubnet", func(t *testing.T) {
		// 10.66.0.1/30: only hosts are .1 (local) and .2 (peer).
		conf := makeConf("10.66.0.1/30", "10.66.0.2")
		assert.Nil(t, conf.computeNetstackDNSIP())
	})

	t.Run("PointToPointSubnet", func(t *testing.T) {
		conf := makeConf("10.66.0.1/31")
		assert.Nil(t, conf.computeNetstackDNSIP())
	})

	t.Run("InvalidSubnet", func(t *testing.T) {
		conf := makeConf("not-a-cidr")
		assert.Nil(t, conf.computeNetstackDNSIP())
	})
}

func TestCheckIPUnique(t *testing.T) {
	conf := &Config{
		VPNConfig: VPNConfig{
			IPNet: "10.66.0.0/16",
		},
		KnownPeers: map[string]KnownPeer{
			"p1": {PeerID: "p1", IPAddr: "10.66.0.1", Alias: "peer1"},
			"p2": {PeerID: "p2", IPAddr: "10.66.0.2", Alias: "peer2"},
		},
	}
	conf.netstackDNSIP = conf.computeNetstackDNSIP()

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
		{"OutsideSubnet", "192.168.1.1", "", "IP 192.168.1.1 does not belong to subnet 10.66.0.0/16"},
		{"BroadcastAddress", "10.66.255.255", "", "IP 10.66.255.255 is the broadcast address of subnet 10.66.0.0/16"},
		{"DNSServerIP", "10.66.255.254", "", "IP 10.66.255.254 is reserved for the awl DNS server"},
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

	t.Run("LocalNodeIPRejected", func(t *testing.T) {
		conf := &Config{
			VPNConfig:  VPNConfig{IPNet: "10.66.0.1/16"},
			KnownPeers: map[string]KnownPeer{},
		}
		conf.netstackDNSIP = conf.computeNetstackDNSIP()

		assert.ErrorContains(t, conf.CheckIPUnique("10.66.0.1", ""), "own address")
		assert.NoError(t, conf.CheckIPUnique("10.66.0.2", ""))
	})

	t.Run("NetstackDNSIPReserved", func(t *testing.T) {
		// Only the fixed netstackDNSIP is reserved: here the interceptor serves
		// on broadcast−2 (e.g. broadcast−1 was taken at startup, then freed), so
		// broadcast−2 is rejected while broadcast−1 is a normal free address.
		conf := &Config{
			VPNConfig:  VPNConfig{IPNet: "10.66.0.0/16"},
			KnownPeers: map[string]KnownPeer{},
		}
		conf.netstackDNSIP = net.ParseIP("10.66.255.253").To4()

		assert.ErrorContains(t, conf.CheckIPUnique("10.66.255.253", ""), "reserved for the awl DNS server")
		assert.NoError(t, conf.CheckIPUnique("10.66.255.254", ""))
		assert.NoError(t, conf.CheckIPUnique("10.66.255.252", ""))

		conf.netstackDNSIP = nil
		assert.NoError(t, conf.CheckIPUnique("10.66.255.253", ""))
	})

	t.Run("ShiftedDNSServerIP", func(t *testing.T) {
		// A peer already holds broadcast−1 (e.g. from an old config), so the
		// DNS IP shifts to broadcast−2: the peer keeps its IP, the shifted
		// reserved address is rejected.
		conf := &Config{
			VPNConfig: VPNConfig{IPNet: "10.66.0.0/16"},
			KnownPeers: map[string]KnownPeer{
				"p1": {PeerID: "p1", IPAddr: "10.66.255.254", Alias: "peer1"},
			},
		}
		conf.netstackDNSIP = conf.computeNetstackDNSIP()

		assert.NoError(t, conf.CheckIPUnique("10.66.255.254", "p1"))
		assert.ErrorContains(t, conf.CheckIPUnique("10.66.255.254", ""), "already used by peer peer1")
		assert.ErrorContains(t, conf.CheckIPUnique("10.66.255.253", ""), "reserved for the awl DNS server")
	})
}
