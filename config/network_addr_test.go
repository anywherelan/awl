package config

import (
	"encoding/json"
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

func TestSetDefaultsIPNet(t *testing.T) {
	cases := []struct {
		name  string
		ipNet string
		// identity gives the config a peer ID, so a zero host part is derived
		identity bool
		want     string
		validErr bool
	}{
		{name: "NewConfigGetsDefault", ipNet: "", want: DefaultVPNNetworkSubnet},
		{name: "DefaultIsDerived", ipNet: "", identity: true, want: testPeerIDDerivedV4 + "/11"},
		{name: "ZeroHostInCustomSubnetIsDerived", ipNet: "192.168.50.0/24", identity: true, want: testPeerIDDerivedV4Custom + "/24"},
		{name: "Legacy16IsWidened", ipNet: "10.66.0.1/16", identity: true, want: migratedIPNet},
		{name: "Legacy24IsWidened", ipNet: "10.66.0.1/24", identity: true, want: migratedIPNet},
		{name: "AddressSetByHandIsKept", ipNet: "10.66.3.7/16", identity: true, want: "10.66.3.7/16"},
		{name: "OtherSubnetIn10_66IsKept", ipNet: "10.66.5.1/24", want: "10.66.5.1/24"},
		{name: "CustomSubnetIsKept", ipNet: "192.168.50.1/24", want: "192.168.50.1/24"},
		{name: "MigratedIsKept", ipNet: migratedIPNet, identity: true, want: migratedIPNet},
		{name: "GarbageFailsValidation", ipNet: "garbage", want: "garbage", validErr: true},
		{name: "NoPrefixLengthFailsValidation", ipNet: "10.66.0.1", want: "10.66.0.1", validErr: true},
		{name: "IPv6FailsValidation", ipNet: "fd00:66::1/48", want: "fd00:66::1/48", validErr: true},
		{name: "IPv4MappedFailsValidation", ipNet: "::ffff:10.66.0.1/107", want: "::ffff:10.66.0.1/107", validErr: true},
		{name: "Slash31FailsValidation", ipNet: "10.66.0.1/31", want: "10.66.0.1/31", validErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := &Config{dataDir: t.TempDir()}
			conf.VPNConfig.IPNet = tc.ipNet
			if tc.identity {
				conf.P2pNode.PeerID = testPeerID
			}
			conf.KnownPeers = map[string]KnownPeer{testPeerID2: {PeerID: testPeerID2}}
			setDefaults(conf, nil)

			assert.Equal(t, tc.want, conf.VPNConfig.IPNet)
			// A peer gets its address on the first status exchange, not on load.
			assert.Empty(t, conf.KnownPeers[testPeerID2].IPAddr)
			if tc.validErr {
				assert.ErrorContains(t, conf.ValidateForStartup(), "vpn.ipNet")
			} else {
				assert.NoError(t, conf.ValidateForStartup())
			}
		})
	}
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
		{name: "NewConfigHasNoIPv6", newConfig: true, want: ""},
		{name: "PrefixWithoutIdentityIsKept", newConfig: true, ipNetV6: DefaultVPNNetworkSubnet6, want: DefaultVPNNetworkSubnet6},
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

func TestDerivedOwnPrefixSkipsReserved(t *testing.T) {
	// In 10.66.0.0/30 testPeerID and testPeerID2 derive the network address
	// .0, testPeerID3 the broadcast address .3: all of them take .1.
	for _, id := range []string{testPeerID, testPeerID2, testPeerID3} {
		got, ok := derivedOwnPrefix(id, netip.MustParsePrefix("10.66.0.0/30"))
		require.True(t, ok)
		assert.Equal(t, "10.66.0.1/30", got.String(), id)
	}

	_, ok := derivedOwnPrefix("", netip.MustParsePrefix(DefaultVPNNetworkSubnet))
	assert.False(t, ok, "no identity yet")
}

func TestSetIdentityDerivesAddrs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)

	conf := &Config{dataDir: dir, VPNConfig: VPNConfig{IPNetV6: DefaultVPNNetworkSubnet6}}
	setDefaults(conf, eventbus.NewBus())
	conf.startWriter()
	t.Cleanup(conf.Close)
	require.Equal(t, DefaultVPNNetworkSubnet, conf.VPNConfig.IPNet)
	require.Equal(t, DefaultVPNNetworkSubnet6, conf.VPNConfig.IPNetV6)

	key, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	conf.SetIdentity(key, id)

	want, ok := derivedOwnPrefix(id.String(), netip.MustParsePrefix(DefaultVPNNetworkSubnet))
	require.True(t, ok)
	assert.Equal(t, want.String(), conf.VPNConfig.IPNet)
	derived, ok := conf.DerivedVPNAddr()
	assert.True(t, ok)
	assert.Equal(t, want.Addr(), derived)

	prefix := netip.MustParsePrefix(DefaultVPNNetworkSubnet6)
	wantV6 := netip.PrefixFrom(deriveAddr(id, prefix), prefix.Bits()).String()
	assert.Equal(t, wantV6, conf.VPNConfig.IPNetV6)

	// Idempotent: our addresses are not re-derived once chosen.
	conf.SetIdentity(key, id)
	assert.Equal(t, want.String(), conf.VPNConfig.IPNet)
	assert.Equal(t, wantV6, conf.VPNConfig.IPNetV6)
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
		assert.Equal(t, "10.95.255.254", conf.computeNetstackDNSIP().String())
	})

	t.Run("CandidateTakenByPeer", func(t *testing.T) {
		conf := makeConf(DefaultVPNNetworkSubnet, "10.95.255.254")
		assert.Equal(t, "10.95.255.253", conf.computeNetstackDNSIP().String())
	})

	t.Run("SeveralCandidatesTakenByPeers", func(t *testing.T) {
		conf := makeConf(DefaultVPNNetworkSubnet, "10.95.255.254", "10.95.255.253")
		assert.Equal(t, "10.95.255.252", conf.computeNetstackDNSIP().String())
	})

	t.Run("CandidateTakenByOwnIP", func(t *testing.T) {
		conf := makeConf("10.66.255.254/16")
		assert.Equal(t, "10.66.255.253", conf.computeNetstackDNSIP().String())
	})

	t.Run("ExhaustedSubnet", func(t *testing.T) {
		// 10.66.0.1/30: only hosts are .1 (local) and .2 (peer).
		conf := makeConf("10.66.0.1/30", "10.66.0.2")
		assert.False(t, conf.computeNetstackDNSIP().IsValid())
	})

	t.Run("InvalidSubnet", func(t *testing.T) {
		conf := makeConf("not-a-cidr")
		assert.False(t, conf.computeNetstackDNSIP().IsValid())
	})
}
