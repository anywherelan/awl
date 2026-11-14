package config

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPeerID  = "12D3KooWBG3PFoGRgbr8ckoRPCpWQjdFj5tME2XBUot5s4uGZkiL"
	testPeerID2 = "12D3KooWGRjpNYgFssihdgTDnr5rdhdh9ruMTbeT41h1fXfGmatZ"
	testPeerID3 = "12D3KooWRXyTH7ZxerZRu6UtYQx62uCmYeZ244SsLQZbjuxX7RrL"

	// testPeerIDDerived is deriveAddr(testPeerID, fd00:66::/48). Pinned: the
	// derivation must not change, or peers would get new addresses on upgrade.
	testPeerIDDerived = "fd00:66:0:7915:10ba:fb1c:ef80:180c"

	// testPeerIDDerivedV4 is deriveAddr(testPeerID, 10.64.0.0/11), pinned for
	// the same reason; testPeerIDDerivedV4Custom is the same in 192.168.50.0/24.
	testPeerIDDerivedV4       = "10.64.129.48"
	testPeerIDDerivedV4Custom = "192.168.50.48"
)

func TestCheckPeerIP(t *testing.T) {
	conf := &Config{
		VPNConfig: VPNConfig{IPNet: "10.66.0.1/16"},
		KnownPeers: map[string]KnownPeer{
			"p2": {PeerID: "p2", IPAddr: "10.66.0.2", Alias: "peer2"},
		},
	}
	conf.netstackDNSIP = conf.computeNetstackDNSIP()
	require.Equal(t, "10.66.255.254", conf.netstackDNSIP.String())

	tests := []struct {
		name         string
		ip           string
		exceptPeerID string
		wantErr      string
	}{
		{"Free", "10.66.0.3", "", ""},
		{"UsedByPeer", "10.66.0.2", "", "IP 10.66.0.2 is already used by peer peer2"},
		{"SamePeer", "10.66.0.2", "p2", ""},
		{"InvalidFormat", "invalid", "", "invalid IP invalid"},
		{"IPv6", "fd00:66::2", "", "IP fd00:66::2 is not an IPv4 address"},
		{"OutsideSubnet", "192.168.1.1", "", "IP 192.168.1.1 does not belong to subnet 10.66.0.0/16"},
		{"NetworkAddress", "10.66.0.0", "", "IP 10.66.0.0 is the network address of subnet 10.66.0.0/16"},
		{"BroadcastAddress", "10.66.255.255", "", "IP 10.66.255.255 is the broadcast address of subnet 10.66.0.0/16"},
		{"DNSServerIP", "10.66.255.254", "", "IP 10.66.255.254 is reserved for the awl DNS server"},
		{"OwnAddress", "10.66.0.1", "", "IP 10.66.0.1 is the local node's own address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conf.CheckPeerIPUnlocked(tt.ip, tt.exceptPeerID)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}

	t.Run("ShiftedDNSServerIP", func(t *testing.T) {
		// A peer already holds broadcast−1 (e.g. from an old config), so the
		// DNS IP shifts to broadcast−2: the peer keeps its IP, the shifted
		// reserved address is rejected.
		conf := &Config{
			VPNConfig: VPNConfig{IPNet: "10.66.0.1/16"},
			KnownPeers: map[string]KnownPeer{
				"p1": {PeerID: "p1", IPAddr: "10.66.255.254", Alias: "peer1"},
			},
		}
		conf.netstackDNSIP = conf.computeNetstackDNSIP()

		assert.NoError(t, conf.CheckPeerIPUnlocked("10.66.255.254", "p1"))
		assert.ErrorContains(t, conf.CheckPeerIPUnlocked("10.66.255.254", ""), "already used by peer peer1")
		assert.ErrorContains(t, conf.CheckPeerIPUnlocked("10.66.255.253", ""), "reserved for the awl DNS server")
	})
}

func TestAllocPeerIP(t *testing.T) {
	newConf := func() *Config {
		conf := &Config{
			VPNConfig: VPNConfig{IPNet: "10.64.0.1/11"},
			KnownPeers: map[string]KnownPeer{
				testPeerID:  {PeerID: testPeerID},
				testPeerID2: {PeerID: testPeerID2, Alias: "other", IPAddr: "10.64.0.2"},
			},
		}
		conf.netstackDNSIP = conf.computeNetstackDNSIP()
		return conf
	}

	t.Run("AnnouncedAccepted", func(t *testing.T) {
		addr, rejectErr, err := newConf().AllocPeerIPUnlocked(testPeerID, "10.80.1.2", nil)
		require.NoError(t, err)
		assert.NoError(t, rejectErr)
		assert.Equal(t, "10.80.1.2", addr.String())
	})

	// Older versions announce nothing, and every config from before v0.21.0
	// has the same own address: both mean the derived address, with no warning.
	for _, announced := range []string{"", legacyOwnIP} {
		t.Run("Derived for "+announced, func(t *testing.T) {
			addr, rejectErr, err := newConf().AllocPeerIPUnlocked(testPeerID, announced, nil)
			require.NoError(t, err)
			assert.NoError(t, rejectErr)
			assert.Equal(t, testPeerIDDerivedV4, addr.String())
		})
	}

	rejected := []struct {
		name      string
		announced string
	}{
		{"Garbage", "garbage"},
		{"IPv6", "fd00:66::5"},
		{"OutsideSubnet", "10.96.0.1"},
		{"NetworkAddress", "10.64.0.0"},
		{"Broadcast", "10.95.255.255"},
		{"DNSServerIP", "10.95.255.254"},
		{"OurAddress", "10.64.0.1"},
		{"OtherPeersAddress", "10.64.0.2"},
	}
	for _, tc := range rejected {
		t.Run("Rejected"+tc.name, func(t *testing.T) {
			addr, rejectErr, err := newConf().AllocPeerIPUnlocked(testPeerID, tc.announced, nil)
			require.NoError(t, err)
			assert.Error(t, rejectErr)
			assert.Equal(t, testPeerIDDerivedV4, addr.String(), "falls back to the derived address")
		})
	}

	derivedNext := netip.MustParseAddr(testPeerIDDerivedV4).Next()

	t.Run("DerivedTakenUsesNext", func(t *testing.T) {
		conf := newConf()
		conf.KnownPeers[testPeerID3] = KnownPeer{PeerID: testPeerID3, IPAddr: testPeerIDDerivedV4}
		addr, _, err := conf.AllocPeerIPUnlocked(testPeerID, "", nil)
		require.NoError(t, err)
		assert.Equal(t, derivedNext, addr)
	})

	t.Run("SuggestedForAnotherPeerSkipped", func(t *testing.T) {
		except := []netip.Addr{netip.MustParseAddr("10.80.1.2"), netip.MustParseAddr(testPeerIDDerivedV4)}
		addr, rejectErr, err := newConf().AllocPeerIPUnlocked(testPeerID, "10.80.1.2", except)
		require.NoError(t, err)
		assert.Error(t, rejectErr)
		assert.Equal(t, derivedNext, addr)
	})
}

func TestAllocPeerIPv6(t *testing.T) {
	newConf := func() *Config {
		return &Config{
			VPNConfig: VPNConfig{IPNetV6: "fd00:66::1/48"},
			KnownPeers: map[string]KnownPeer{
				testPeerID:  {PeerID: testPeerID},
				testPeerID2: {PeerID: testPeerID2, Alias: "other", IPAddrV6: "fd00:66::2"},
			},
		}
	}

	t.Run("AnnouncedAccepted", func(t *testing.T) {
		addr, rejectErr, err := newConf().AllocPeerIPv6Unlocked(testPeerID, "fd00:66::5")
		require.NoError(t, err)
		assert.NoError(t, rejectErr)
		assert.Equal(t, "fd00:66::5", addr.String())
	})

	t.Run("AnnouncedCanonicalized", func(t *testing.T) {
		addr, rejectErr, err := newConf().AllocPeerIPv6Unlocked(testPeerID, "FD00:0066:0:0:0:0:0:0005")
		require.NoError(t, err)
		assert.NoError(t, rejectErr)
		assert.Equal(t, "fd00:66::5", addr.String())
	})

	t.Run("AnnouncedEqualsOwnStoredAddress", func(t *testing.T) {
		conf := newConf()
		conf.KnownPeers[testPeerID] = KnownPeer{PeerID: testPeerID, IPAddrV6: "fd00:66::5"}
		addr, rejectErr, err := conf.AllocPeerIPv6Unlocked(testPeerID, "fd00:66::5")
		require.NoError(t, err)
		assert.NoError(t, rejectErr)
		assert.Equal(t, "fd00:66::5", addr.String())
	})

	rejected := []struct {
		name      string
		announced string
	}{
		{"Garbage", "garbage"},
		{"IPv4", "10.66.0.3"},
		{"IPv4Mapped", "::ffff:10.66.0.3"},
		{"WithZone", "fd00:66::5%eth0"},
		{"OutsideSubnet", "fd00:77::5"},
		{"PublicAddress", "2606:4700:4700::1111"},
		{"Loopback", "::1"},
		{"SubnetRouterAnycast", "fd00:66::"},
		{"OurAddress", "fd00:66::1"},
		{"OtherPeersAddress", "fd00:66::2"},
		{"OtherPeersAddressNonCanonical", "fd00:66:0:0:0:0:0:2"},
	}
	for _, tc := range rejected {
		t.Run("Rejected"+tc.name, func(t *testing.T) {
			addr, rejectErr, err := newConf().AllocPeerIPv6Unlocked(testPeerID, tc.announced)
			require.NoError(t, err)
			assert.Error(t, rejectErr)
			assert.Equal(t, testPeerIDDerived, addr.String(), "falls back to the derived address")
		})
	}

	t.Run("DerivedTakenUsesNext", func(t *testing.T) {
		conf := newConf()
		conf.KnownPeers[testPeerID3] = KnownPeer{PeerID: testPeerID3, IPAddrV6: testPeerIDDerived}
		addr, rejectErr, err := conf.AllocPeerIPv6Unlocked(testPeerID, "fd00:77::5")
		require.NoError(t, err)
		assert.Error(t, rejectErr)
		assert.Equal(t, "fd00:66:0:7915:10ba:fb1c:ef80:180d", addr.String())
	})

	t.Run("IPv6Disabled", func(t *testing.T) {
		conf := newConf()
		conf.VPNConfig.IPNetV6 = ""
		_, _, err := conf.AllocPeerIPv6Unlocked(testPeerID, "fd00:66::5")
		assert.ErrorIs(t, err, ErrIPv6Disabled)
	})
}

func TestPeerIPv6(t *testing.T) {
	conf := &Config{VPNConfig: VPNConfig{IPNetV6: "fd00:66::1/48"}}

	cases := []struct {
		name string
		peer KnownPeer
		want string // empty: not usable
	}{
		{"Usable", KnownPeer{RemoteIPv6Enabled: true, IPAddrV6: "fd00:66::5"}, "fd00:66::5"},
		{"Canonicalized", KnownPeer{RemoteIPv6Enabled: true, IPAddrV6: "FD00:66:0::5"}, "fd00:66::5"},
		{"PeerDisabledIPv6", KnownPeer{RemoteIPv6Enabled: false, IPAddrV6: "fd00:66::5"}, ""},
		{"NoAddress", KnownPeer{RemoteIPv6Enabled: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, ok := conf.PeerIPv6Unlocked(tc.peer)
			if tc.want == "" {
				assert.False(t, ok)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, tc.want, addr.String())
		})
	}

	t.Run("OurIPv6Disabled", func(t *testing.T) {
		conf := &Config{}
		_, ok := conf.PeerIPv6Unlocked(KnownPeer{RemoteIPv6Enabled: true, IPAddrV6: "fd00:66::5"})
		assert.False(t, ok)
	})
}

func TestPickAddrNoFreeAddress(t *testing.T) {
	errTaken := errors.New("taken")

	_, rejectErr, err := pickAddr(testPeerID, netip.MustParsePrefix("fd00:66::/48"), "fd00:66::5",
		func(netip.Addr) error { return errTaken })
	assert.ErrorIs(t, rejectErr, errTaken)
	assert.Error(t, err)
}

func TestDeriveAddr(t *testing.T) {
	id, err := peer.Decode(testPeerID)
	require.NoError(t, err)

	cases := []struct {
		prefix string
		want   string
	}{
		{"fd00:66::/48", testPeerIDDerived},
		// the host part of the prefix is ignored
		{"fd00:66::5/48", testPeerIDDerived},
		{"fd00:66::/64", "fd00:66::10ba:fb1c:ef80:180c"},
		// not byte-aligned: the low nibble of the 7th byte comes from the hash
		{"fd00:66::/52", "fd00:66:0:915:10ba:fb1c:ef80:180c"},
	}
	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			got := deriveAddr(id, netip.MustParsePrefix(tc.prefix))
			assert.Equal(t, tc.want, got.String())
		})
	}

	t.Run("IPv4", func(t *testing.T) {
		assert.Equal(t, testPeerIDDerivedV4, deriveAddr(id, netip.MustParsePrefix(DefaultVPNNetworkSubnet)).String())
		assert.Equal(t, testPeerIDDerivedV4, deriveAddr(id, netip.MustParsePrefix("10.66.0.1/11")).String(),
			"the host part of the prefix is ignored")
	})

	t.Run("DifferentPeers", func(t *testing.T) {
		id2, err := peer.Decode(testPeerID2)
		require.NoError(t, err)
		prefix := netip.MustParsePrefix("fd00:66::/48")
		assert.NotEqual(t, deriveAddr(id, prefix), deriveAddr(id2, prefix))
	})
}

func TestNextAddr(t *testing.T) {
	cases := []struct {
		addr   string
		prefix string
		want   string
	}{
		{"fd00:66::1", "fd00:66::/48", "fd00:66::2"},
		{"fd00:66::ffff", "fd00:66::/112", "fd00:66::"},
		{"fd00:66:0:ffff:ffff:ffff:ffff:ffff", "fd00:66::/48", "fd00:66::"},
		{"10.66.0.1", "10.66.0.0/16", "10.66.0.2"},
		{"10.66.255.255", "10.66.0.0/16", "10.66.0.0"},
	}
	for _, tc := range cases {
		got := nextAddr(netip.MustParseAddr(tc.addr), netip.MustParsePrefix(tc.prefix))
		assert.Equal(t, tc.want, got.String(), "%s in %s", tc.addr, tc.prefix)
	}
}
