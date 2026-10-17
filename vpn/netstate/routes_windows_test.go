//go:build windows

package netstate

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// TestClientFenceRules pins the shape of the leak-fence rule set: per family
// exactly three PERMITs above one unconditional BLOCK, with the exemptions
// the fence design promises (own app, tunnel egress, local destinations).
// The rules are pure data — no filtering engine is touched here; the live
// behaviour is covered by the hostnet suite.
func TestClientFenceRules(t *testing.T) {
	sublayerGUID, err := windows.GenerateGUID()
	require.NoError(t, err)
	sublayer := wf.SublayerID(sublayerGUID)
	tunLUID := winipcfg.LUID(0x1234567890abcdef)
	const appID = `\device\harddiskvolume3\awl\awl.exe`

	rules, err := clientFenceRules(sublayer, tunLUID, appID)
	require.NoError(t, err)
	require.Len(t, rules, 8, "3 permits + 1 block per family")

	byLayer := map[wf.LayerID][]*wf.Rule{}
	seenIDs := map[wf.RuleID]bool{}
	for _, r := range rules {
		require.Equal(t, sublayer, r.Sublayer, "rule %q must live in our sublayer", r.Name)
		require.Contains(t, r.Name, wfpClientRulePrefix, "rule %q must carry the lookup prefix", r.Name)
		require.False(t, seenIDs[r.ID], "rule IDs must be unique")
		seenIDs[r.ID] = true
		byLayer[r.Layer] = append(byLayer[r.Layer], r)
	}
	require.Len(t, byLayer[wf.LayerALEAuthConnectV4], 4)
	require.Len(t, byLayer[wf.LayerALEAuthConnectV6], 4)

	for _, layer := range []wf.LayerID{wf.LayerALEAuthConnectV4, wf.LayerALEAuthConnectV6} {
		var block *wf.Rule
		for _, r := range byLayer[layer] {
			if r.Action == wf.ActionBlock {
				require.Nil(t, block, "exactly one BLOCK per layer")
				block = r
				continue
			}
			require.Equal(t, wf.ActionPermit, r.Action)
		}
		require.NotNil(t, block)
		require.Empty(t, block.Conditions, "the BLOCK must be unconditional")
		for _, r := range byLayer[layer] {
			if r.Action == wf.ActionPermit {
				require.Greater(t, r.Weight, block.Weight,
					"every PERMIT must outrank the BLOCK inside the sublayer")
			}
		}
	}

	// Exemption conditions: own app and tunnel egress, per family.
	requireCondition := func(namePart string, check func(r *wf.Rule)) {
		t.Helper()
		found := 0
		for _, r := range rules {
			if r.Action == wf.ActionPermit && strings.Contains(r.Name, namePart) {
				check(r)
				found++
			}
		}
		require.Equal(t, 2, found, "permit %q must exist in both families", namePart)
	}
	requireCondition("permit awl process", func(r *wf.Rule) {
		require.Len(t, r.Conditions, 1)
		require.Equal(t, wf.FieldALEAppID, r.Conditions[0].Field)
		require.Equal(t, appID, r.Conditions[0].Value)
	})
	requireCondition("permit tunnel egress", func(r *wf.Rule) {
		require.Len(t, r.Conditions, 1)
		require.Equal(t, wf.FieldIPLocalInterface, r.Conditions[0].Field)
		require.Equal(t, uint64(tunLUID), r.Conditions[0].Value)
	})

	// Local-destinations permit: the complete address set per family.
	wantLocal4 := slices.Concat(
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		privateSubnetPrefixes(),
		[]netip.Prefix{
			netip.MustParsePrefix("224.0.0.0/4"),
			netip.MustParsePrefix("255.255.255.255/32"),
		},
	)
	wantLocal6 := []netip.Prefix{
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("ff00::/8"),
	}
	requireLocalDst := func(layer wf.LayerID, want []netip.Prefix) {
		t.Helper()
		for _, r := range byLayer[layer] {
			if !strings.Contains(r.Name, "permit local destinations") {
				continue
			}
			var got []netip.Prefix
			for _, c := range r.Conditions {
				require.Equal(t, wf.FieldIPRemoteAddress, c.Field)
				got = append(got, c.Value.(netip.Prefix))
			}
			require.ElementsMatch(t, want, got)
			return
		}
		t.Fatalf("no local-destinations permit in layer %v", layer)
	}
	requireLocalDst(wf.LayerALEAuthConnectV4, wantLocal4)
	requireLocalDst(wf.LayerALEAuthConnectV6, wantLocal6)
}
