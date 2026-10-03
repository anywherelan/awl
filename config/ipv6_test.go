package config

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
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
)

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
		prefix := netip.MustParsePrefix("10.66.0.0/16")
		got := deriveAddr(id, prefix)
		assert.True(t, got.Is4())
		assert.True(t, prefix.Contains(got))
		assert.Equal(t, got, deriveAddr(id, prefix), "must be deterministic")
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

func TestPickPeerAddrNoFreeAddress(t *testing.T) {
	id, err := peer.Decode(testPeerID)
	require.NoError(t, err)
	errTaken := errors.New("taken")

	_, rejectErr, err := pickPeerAddr(id, netip.MustParsePrefix("fd00:66::/48"), "fd00:66::5",
		func(netip.Addr) error { return errTaken })
	assert.ErrorIs(t, rejectErr, errTaken)
	assert.Error(t, err)
}

func TestParseIPNetV6(t *testing.T) {
	valid := []string{
		DefaultVPNNetworkSubnet6,
		"fd00:66::/48",
		"fd00:66::/64",
		"fd00:66::5/48",
		testPeerIDDerived + "/48",
	}
	for _, s := range valid {
		_, err := parseIPNetV6(s)
		assert.NoError(t, err, s)
	}

	invalid := []string{
		"",
		"garbage",
		"fd00:66::",
		"10.66.0.1/16",
		"::ffff:10.66.0.1/112",
		"fd00:66::/96",
		"fd00:66::/128",
	}
	for _, s := range invalid {
		_, err := parseIPNetV6(s)
		assert.Error(t, err, s)
	}
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
		assert.Equal(t, "fd00:66::5", addr)
	})

	t.Run("AnnouncedCanonicalized", func(t *testing.T) {
		addr, rejectErr, err := newConf().AllocPeerIPv6Unlocked(testPeerID, "FD00:0066:0:0:0:0:0:0005")
		require.NoError(t, err)
		assert.NoError(t, rejectErr)
		assert.Equal(t, "fd00:66::5", addr)
	})

	t.Run("AnnouncedEqualsOwnStoredAddress", func(t *testing.T) {
		conf := newConf()
		conf.KnownPeers[testPeerID] = KnownPeer{PeerID: testPeerID, IPAddrV6: "fd00:66::5"}
		addr, rejectErr, err := conf.AllocPeerIPv6Unlocked(testPeerID, "fd00:66::5")
		require.NoError(t, err)
		assert.NoError(t, rejectErr)
		assert.Equal(t, "fd00:66::5", addr)
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
			assert.Equal(t, testPeerIDDerived, addr, "falls back to the derived address")
		})
	}

	t.Run("DerivedTakenUsesNext", func(t *testing.T) {
		conf := newConf()
		conf.KnownPeers[testPeerID3] = KnownPeer{PeerID: testPeerID3, IPAddrV6: testPeerIDDerived}
		addr, rejectErr, err := conf.AllocPeerIPv6Unlocked(testPeerID, "fd00:77::5")
		require.NoError(t, err)
		assert.Error(t, rejectErr)
		assert.Equal(t, "fd00:66:0:7915:10ba:fb1c:ef80:180d", addr)
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

func TestDNSNamesMappingV6(t *testing.T) {
	conf := &Config{
		VPNConfig: VPNConfig{IPNetV6: "fd00:66::1/48"},
		KnownPeers: map[string]KnownPeer{
			testPeerID:  {PeerID: testPeerID, DomainName: "one", RemoteIPv6Enabled: true, IPAddrV6: "FD00:66::5"},
			testPeerID2: {PeerID: testPeerID2, DomainName: "two", RemoteIPv6Enabled: false, IPAddrV6: "fd00:66::6"},
		},
	}

	assert.Equal(t, map[string]netip.Addr{
		testPeerID: netip.MustParseAddr("fd00:66::5"),
		"one":      netip.MustParseAddr("fd00:66::5"),
	}, conf.DNSNamesMappingV6())
}

func TestSetDefaultsIPv6(t *testing.T) {
	cases := []struct {
		name string
		// newConfig: no config file yet (empty Version) and no identity
		newConfig bool
		ipNetV6   string
		want      string
		validErr  bool
	}{
		{name: "NewConfigWithoutIdentity", newConfig: true, want: DefaultVPNNetworkSubnet6},
		{name: "ExistingConfigStaysWithoutIPv6", want: ""},
		{name: "DefaultPrefixIsDerived", ipNetV6: DefaultVPNNetworkSubnet6, want: testPeerIDDerived + "/48"},
		{name: "CustomPrefixIsDerived", ipNetV6: "fd00:66::/64", want: "fd00:66::10ba:fb1c:ef80:180c/64"},
		{name: "ExplicitAddressIsKept", ipNetV6: "fd00:66::5/48", want: "fd00:66::5/48"},
		{name: "InvalidIsKeptAndFailsValidation", ipNetV6: "10.66.0.0/16", want: "10.66.0.0/16", validErr: true},
		{name: "GarbageIsKeptAndFailsValidation", ipNetV6: "garbage", want: "garbage", validErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := &Config{dataDir: t.TempDir()}
			conf.VPNConfig.IPNetV6 = tc.ipNetV6
			if !tc.newConfig {
				conf.Version = Version
				conf.P2pNode.PeerID = testPeerID
			}
			setDefaults(conf, nil)

			assert.Equal(t, tc.want, conf.VPNConfig.IPNetV6)
			if tc.validErr {
				assert.ErrorContains(t, conf.ValidateForStartup(), "vpn.ipNetV6")
			} else {
				assert.NoError(t, conf.ValidateForStartup())
			}
		})
	}
}

// TestLoadConfigInvalidIPNetV6 pins why an invalid vpnConfig.ipNetV6 is
// reported by ValidateForStartup and not by LoadConfig: callers replace a
// config that fails to load with a fresh one and overwrite the file, which
// would cost the node its identity. So the config must load with the value
// untouched, and saving it must not change the file's meaning either.
func TestLoadConfigInvalidIPNetV6(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)
	configPath := filepath.Join(dir, AppConfigFilename)

	const identity = "test-identity"
	const invalidIPNetV6 = "10.66.0.0/16"
	stored := &Config{Version: Version}
	stored.P2pNode.PeerID = testPeerID
	stored.P2pNode.Identity = identity
	stored.VPNConfig.IPNetV6 = invalidIPNetV6
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, filesPerm))

	conf, err := LoadConfig(AppTypeAwl, eventbus.NewBus())
	require.NoError(t, err)
	assert.ErrorContains(t, conf.ValidateForStartup(), "vpn.ipNetV6")

	// Application.Close saves the config even when Init has failed.
	conf.Save()
	conf.Close()

	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	saved := &Config{}
	require.NoError(t, json.Unmarshal(data, saved))
	assert.Equal(t, invalidIPNetV6, saved.VPNConfig.IPNetV6)
	assert.Equal(t, testPeerID, saved.P2pNode.PeerID)
	assert.Equal(t, identity, saved.P2pNode.Identity)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the config must not be backed up as corrupted")
}

func TestSetIdentityDerivesIPv6(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)

	conf := &Config{dataDir: dir}
	setDefaults(conf, eventbus.NewBus())
	conf.startWriter()
	t.Cleanup(conf.Close)
	require.Equal(t, DefaultVPNNetworkSubnet6, conf.VPNConfig.IPNetV6)

	key, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	conf.SetIdentity(key, id)

	prefix := netip.MustParsePrefix(DefaultVPNNetworkSubnet6)
	want := netip.PrefixFrom(deriveAddr(id, prefix), prefix.Bits()).String()
	assert.Equal(t, want, conf.VPNConfig.IPNetV6)

	// Idempotent: our address is not re-derived once chosen.
	conf.SetIdentity(key, id)
	assert.Equal(t, want, conf.VPNConfig.IPNetV6)
}
