//go:build windows

package netstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const (
	wfpSublayerName = "awl-gateway-server"
	wfpRuleName     = "awl-gateway-server: block forwarded LAN/CGNAT egress from TUN"

	// winNATName is the name of the WinNAT instance owned by awl.
	winNATName = "awl-gateway"

	// wfpSublayerWeight is the weight of both awl WFP sublayers (the server
	// forward-layer filter and the client leak fence). 0x8000 is the midpoint
	// of the uint16 range: sublayers arbitrate high-to-low, and our BLOCK
	// rules lose only to a hard permit (FWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT)
	// from a higher-weight sublayer. The midpoint deliberately does NOT claim
	// the top: these filters fence awl's own traffic, they are not a host
	// kill-switch, so a security product that outranks us on purpose (EDR,
	// corporate firewall) is allowed to win. Contrast wireguard-windows,
	// whose firewall takes 0xFFFF exactly because it IS a kill-switch.
	wfpSublayerWeight = 0x8000
)

// natState holds the state needed to teardown NAT on Windows.
//
// Crash semantics (kill -9): the WFP session below is Dynamic, so the kernel
// removes its sublayer and filter the moment the process dies — no stale
// firewall state is possible. The WinNAT instance and the forwarding flags do
// survive a crash, but they cannot cause transit on their own: the exit-node
// data path runs through this userspace process (libp2p → TUN write) and the
// Wintun adapter disappears with it. The leftover NetNat is removed by
// stale-recovery on the next setupNAT; the forwarding flags are left as-is
// forever — their provenance is unknown after a crash (the next run sees
// "enabled" and treats it as "was already enabled"), same caveat as the Linux
// ip_forward handling.
type natState struct {
	natCreated bool
	wfpSession *wf.Session
	// forwardingEnabled lists interfaces where WE flipped IPv4 forwarding on
	// (it was off before setupNAT or before a forwarding re-sync). Only these
	// are reverted at teardown; roaming accumulates LUIDs here, all reverted
	// together in DisableServerNAT.
	forwardingEnabled []winipcfg.LUID

	// tunLUID is the TUN adapter, resolved once in setupNAT; used by the
	// forwarding re-sync as the uplink-scan exclusion.
	tunLUID winipcfg.LUID
	// uplinkLUID is the uplink whose forwarding the exit node currently
	// relies on (0 = none — offline start). Compared by LUID, not IfIndex
	// (Windows reuses interface indexes after adapter removal), and updated
	// by resyncServerForwarding when the best default route moves.
	uplinkLUID winipcfg.LUID
}

// setupNAT configures the Windows exit node: a WFP forward-layer filter that
// keeps clients out of the exit node's LAN/CGNAT space, per-interface IPv4
// forwarding on the TUN and the uplink, and a WinNAT instance providing
// MASQUERADE-style translation for the awl subnet.
//
// Order matters and is fail-closed: the WFP BLOCK filter is installed before
// forwarding/NAT create any technical possibility of transit. Any failure
// after the first step rolls back via teardownNAT (all steps are idempotent).
func (m *Manager) setupNAT(awlSubnet, awlSubnet6, tunIfName string) (*natState, error) {
	awlPrefix, err := netip.ParsePrefix(awlSubnet)
	if err != nil {
		return nil, fmt.Errorf("parse awl subnet %q: %w", awlSubnet, err)
	}
	tunLUID, err := luidFromGUIDName(tunIfName)
	if err != nil {
		return nil, fmt.Errorf("resolve TUN interface: %w", err)
	}
	tunIfRow, err := tunLUID.Interface()
	if err != nil {
		return nil, fmt.Errorf("query TUN interface row: %w", err)
	}
	tunIfIndex := tunIfRow.InterfaceIndex

	// Stale recovery: a previous run killed before teardownNAT leaves its
	// NetNat behind (WFP leftovers are impossible — dynamic session). Remove
	// it so New-NetNat below gets a clean slate.
	cleaned, err := cleanupStaleWinNAT()
	if winNATUnavailable(err) {
		// Not a cleanup problem: WinNAT does not exist on this host at all,
		// and the "pre-clean" framing would only obscure that.
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("pre-clean stale WinNAT: %w", err)
	}
	if cleaned {
		logger.Warnf("recovered from leftover gateway NAT state (previous run was likely killed before teardown)")
	}

	state := &natState{tunLUID: tunLUID}

	// 1. WFP forward-layer BLOCK — the safety fence goes up first.
	if err := setupWFP(state, tunIfIndex, awlPrefix); err != nil {
		_ = m.teardownNAT(state)
		return nil, fmt.Errorf("setup WFP filter: %w", err)
	}

	// 2. Per-interface IPv4 forwarding on TUN + uplink. The uplink is the
	// interface holding the best default route right now; if the host is
	// offline we still bring the server up (parity with Linux, where
	// ServerEnabled is applied at startup regardless of connectivity) and
	// only warn — the network-change watcher re-syncs forwarding onto the
	// uplink when one appears or changes (resyncServerForwarding).
	forwardingTargets := []winipcfg.LUID{tunLUID}
	route, ok, err := bestUplinkDefault(windows.AF_INET, tunLUID)
	if err != nil {
		_ = m.teardownNAT(state)
		return nil, fmt.Errorf("detect uplink for forwarding: %w", err)
	}
	if ok {
		state.uplinkLUID = winipcfg.LUID(route.IfLUID)
		forwardingTargets = append(forwardingTargets, state.uplinkLUID)
	} else {
		logger.Warnf("no IPv4 uplink right now: enabling forwarding on TUN only, " +
			"internet transit will start automatically when network appears")
	}
	for _, luid := range forwardingTargets {
		enabledByUs, err := enableIPv4Forwarding(luid)
		if err != nil {
			_ = m.teardownNAT(state)
			return nil, fmt.Errorf("enable forwarding on interface %d: %w", luid, err)
		}
		if enabledByUs {
			state.forwardingEnabled = append(state.forwardingEnabled, luid)
		}
	}

	// 3. WinNAT instance — transit becomes possible only now, with the WFP
	// fence already in place.
	if err := createWinNAT(awlSubnet); err != nil {
		_ = m.teardownNAT(state)
		return nil, err
	}
	state.natCreated = true

	return state, nil
}

// teardownNAT reverses setupNAT in reverse order: WinNAT first (internet
// transit dies), then forwarding (LAN transit dies), then the WFP session —
// the protection is removed last, when transit is no longer possible. Errors
// are collected; teardown proceeds through all steps. Safe on partial state.
func (m *Manager) teardownNAT(state *natState) error {
	if state == nil {
		return nil
	}

	var errs []error
	if state.natCreated {
		if err := removeWinNAT(); err != nil {
			errs = append(errs, fmt.Errorf("remove WinNAT: %w", err))
		}
		state.natCreated = false
	}

	for _, luid := range state.forwardingEnabled {
		if err := disableIPv4Forwarding(luid); err != nil {
			errs = append(errs, fmt.Errorf("restore forwarding on interface %d: %w", luid, err))
		}
	}
	state.forwardingEnabled = nil

	if state.wfpSession != nil {
		if err := state.wfpSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close WFP session: %w", err))
		}
		state.wfpSession = nil
	}

	return errors.Join(errs...)
}

// resyncServerForwarding is the exit node's reaction to a (debounced) network
// change: when the best IPv4 uplink appears or moves to another interface,
// enable IPv4 forwarding on it so internet transit survives exit-node roaming
// and an offline server start heals itself once connectivity appears. Called
// from the watch goroutine (see onNetworkChange); a no-op while the server is
// disabled. Only forwarding is uplink-bound and needs this: the WinNAT
// instance is tied to the awl subnet and the WFP filter to the TUN ifIndex.
func (m *Manager) resyncServerForwarding() {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.natState
	if state == nil {
		return
	}
	route, ok, err := bestUplinkDefault(windows.AF_INET, state.tunLUID)
	if err != nil {
		logger.Errorf("gateway server forwarding re-sync: detect uplink: %v", err)
		return
	}
	resyncForwarding(state, route, ok, enableIPv4Forwarding)
}

// resyncForwarding is the decision half of resyncServerForwarding, split from
// the live table scan and the lock so it can be unit-tested with a fake
// enable. Caller holds Manager.mu.
//
// Semantics:
//   - no uplink (ok=false): keep state as-is. Forwarding on the abandoned
//     uplink is deliberately NOT disabled — another consumer (ICS, containers,
//     other VPNs) may rely on it; a spurious flag is the lesser evil. It is
//     reverted at teardown iff we enabled it.
//   - same uplink: no-op. In particular a forwarding flag flipped off
//     externally on the current uplink is NOT re-enabled (we don't fight other
//     software — same hands-off stance as the Linux ip_forward handling).
//   - enable failure: uplinkLUID is left unchanged so the next network event
//     retries.
func resyncForwarding(state *natState, route uplinkRoute, ok bool, enable func(winipcfg.LUID) (bool, error)) {
	if !ok {
		return
	}
	newLUID := winipcfg.LUID(route.IfLUID)
	if newLUID == state.uplinkLUID {
		return
	}

	enabledByUs, err := enable(newLUID)
	if err != nil {
		logger.Errorf("gateway server forwarding re-sync: enable forwarding on new uplink (ifIndex %d): %v",
			route.IfIndex, err)
		return
	}
	// Membership check: roaming back to an uplink we already enabled once
	// (and whose forwarding got flipped off in between) must not record a
	// second teardown entry.
	if enabledByUs && !slices.Contains(state.forwardingEnabled, newLUID) {
		state.forwardingEnabled = append(state.forwardingEnabled, newLUID)
	}
	state.uplinkLUID = newLUID
	logger.Infof("gateway server: uplink changed, IPv4 forwarding ensured on new uplink (ifIndex %d)", route.IfIndex)
}

// setupWFP opens a dynamic WFP session and installs one BLOCK filter on the
// IPv4 forward layer:
//
//	sourceInterfaceIndex == TUN  AND  src ∈ awlSubnet  AND  dst ∈ privateSubnets
//
// (multiple conditions on one field OR together, distinct fields AND).
//
// The load-bearing condition is SOURCE_INTERFACE_INDEX: the input interface
// of a forwarded packet is not rewritten by NAT, so the block holds regardless
// of how WinNAT translation is ordered against forward-layer classification —
// the same semantics as the Linux `-i awl0` jump. src ∈ awlSubnet is the
// second belt (parity with `-s awlSubnet`).
//
// No conntrack analogue is needed: reply traffic (src external, dst ∈
// awlSubnet) enters through the uplink, not the TUN, and matches neither of
// the first two conditions.
//
// No IPv6 filter: the client does not tunnel IPv6 (writeInboundBatch drops
// IsIPv6), so IPv6 transit from awl clients does not exist.
//
// Known WFP limitation (accepted): a BLOCK from our sublayer can only be
// overridden by a hard permit (FWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT) from a
// higher-weight sublayer — exotic on the forward layer; a plain PERMIT from
// another firewall does not defeat it.
//
// The session is Dynamic: everything installed here is removed by the kernel
// when the session closes or the process dies, so there is no stale-WFP
// recovery path.
func setupWFP(state *natState, tunIfIndex uint32, awlPrefix netip.Prefix) error {
	session, sublayerID, err := openDynamicWFPSession(
		"Anywherelan VPN gateway",
		"Blocks forwarded traffic from awl devices to the exit node's private networks",
		wfpSublayerName,
	)
	if err != nil {
		return err
	}
	// Parked in state immediately, so on any later failure the caller's
	// rollback (teardownNAT) closes the session — the same idiom as the
	// forwarding and WinNAT steps. A dangling dynamic session would otherwise
	// outlive its guarantees until process exit.
	state.wfpSession = session

	ruleID, err := newWFPGUID()
	if err != nil {
		return err
	}
	conditions := []*wf.Match{
		{Field: wf.FieldSourceInterfaceIndex, Op: wf.MatchTypeEqual, Value: tunIfIndex},
		{Field: wf.FieldIPSourceAddress, Op: wf.MatchTypeEqual, Value: awlPrefix},
	}
	for _, p := range privateSubnetPrefixes() {
		conditions = append(conditions, &wf.Match{
			Field: wf.FieldIPDestinationAddress, Op: wf.MatchTypeEqual, Value: p,
		})
	}
	// The filter weight arbitrates only among filters INSIDE our own
	// sublayer, and we install exactly one — any non-zero value behaves
	// identically. 1000 just leaves room on both sides for future rules.
	err = session.AddRule(&wf.Rule{
		ID:         wf.RuleID(ruleID),
		Name:       wfpRuleName,
		Layer:      wf.LayerIPForwardV4,
		Sublayer:   sublayerID,
		Weight:     1000,
		Conditions: conditions,
		Action:     wf.ActionBlock,
	})
	if err != nil {
		return fmt.Errorf("add WFP block rule: %w", err)
	}

	return nil
}

// openDynamicWFPSession opens a dynamic WFP session and creates one sublayer
// in it at wfpSublayerWeight, returning both. Shared by the server
// forward-layer filter (setupWFP) and the client leak fence
// (setupClientFence). Dynamic: the kernel removes the sublayer and every rule
// under it when the session closes or the process dies, so there is no
// stale-WFP recovery path on either side. On failure the session is closed
// here; on success the caller must park it where its teardown can close it.
func openDynamicWFPSession(name, description, sublayerName string) (*wf.Session, wf.SublayerID, error) {
	session, err := wf.New(&wf.Options{
		Name:        name,
		Description: description,
		Dynamic:     true,
	})
	if err != nil {
		return nil, wf.SublayerID{}, fmt.Errorf("open WFP session: %w", err)
	}

	sublayerGUID, err := newWFPGUID()
	if err != nil {
		_ = session.Close()
		return nil, wf.SublayerID{}, err
	}
	sublayerID := wf.SublayerID(sublayerGUID)
	if err := session.AddSublayer(&wf.Sublayer{
		ID:     sublayerID,
		Name:   sublayerName,
		Weight: wfpSublayerWeight,
	}); err != nil {
		_ = session.Close()
		return nil, wf.SublayerID{}, fmt.Errorf("add WFP sublayer: %w", err)
	}
	return session, sublayerID, nil
}

func newWFPGUID() (windows.GUID, error) {
	guid, err := windows.GenerateGUID()
	if err != nil {
		return windows.GUID{}, fmt.Errorf("generate GUID: %w", err)
	}
	return guid, nil
}

// enableIPv4Forwarding turns IPv4 forwarding on for one interface via
// SetIpInterfaceEntry. Returns enabledByUs=true iff it was off and we flipped
// it — mirroring the Linux ip_forward logic: interfaces that already forward
// (other VPNs, containers, ICS) are left alone at teardown too.
func enableIPv4Forwarding(luid winipcfg.LUID) (bool, error) {
	ipIface, err := luid.IPInterface(windows.AF_INET)
	if err != nil {
		return false, fmt.Errorf("get IP interface: %w", err)
	}
	if ipIface.ForwardingEnabled {
		return false, nil
	}
	ipIface.ForwardingEnabled = true
	// winipcfg's IPInterface() already normalizes SitePrefixLength for the
	// SetIpInterfaceEntry quirk, so the row is safe to Set as-is.
	if err := ipIface.Set(); err != nil {
		return false, fmt.Errorf("set forwarding: %w", err)
	}
	return true, nil
}

func disableIPv4Forwarding(luid winipcfg.LUID) error {
	ipIface, err := luid.IPInterface(windows.AF_INET)
	if err != nil {
		return fmt.Errorf("get IP interface: %w", err)
	}
	if !ipIface.ForwardingEnabled {
		return nil
	}
	ipIface.ForwardingEnabled = false
	if err := ipIface.Set(); err != nil {
		return fmt.Errorf("set forwarding: %w", err)
	}
	return nil
}

// powerShellTimeout bounds one runPowerShell invocation. Generous: a cold
// PowerShell start plus New-NetNat normally fits in a few seconds, so a
// minute only ever fires on a genuine hang.
const powerShellTimeout = time.Minute

// runPowerShell executes one PowerShell command line. -Command is unaffected
// by execution policy; powershell.exe is present down to Server Core.
//
// The timeout matters for liveness, not just hygiene: PowerShell can hang
// (WinNAT's WinRT plumbing has been seen blocking), the callers run under
// m.mu, and since the forwarding re-sync runs on the watch goroutine, a
// wedged PowerShell would freeze socket re-binding along with all gateway
// operations. The timeout turns that into an error.
func runPowerShell(command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), powerShellTimeout)
	defer cancel()
	// The OutputEncoding prefix forces UTF-8 on the redirected pipes: without
	// it PowerShell encodes its (localized) error text in the console
	// codepage, which mangles non-ASCII into '?' by the time it reaches logs.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; "+command)
	// awl runs as a GUI process with no console of its own, so a bare
	// powershell.exe would get a fresh visible console window.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("powershell %q: %w (output: %s)", command, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// winNATUnavailable reports whether err is WMI's WBEM_E_INVALID_CLASS from a
// *-NetNat cmdlet: the MSFT_NetNat class does not exist on this installation
// at all. Windows Home editions do not ship WinNAT, and on other editions the
// class can be missing when neither Hyper-V nor RAS components are enabled or
// the WMI repository is corrupted. Matched by HRESULT, not message text — the
// text is localized.
func winNATUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "0x80041010")
}

// winNATUnavailableError replaces the raw multi-line PowerShell dump with an
// actionable message for the winNATUnavailable case. The original error is
// deliberately not wrapped: the HRESULT is the whole story, and this text is
// shown verbatim in the UI.
func winNATUnavailableError() error {
	return errors.New("WinNAT is not available on this Windows installation " +
		"(WMI class MSFT_NetNat does not exist, HRESULT 0x80041010). " +
		"It is missing on Windows Home editions; on other editions it requires Hyper-V or RAS " +
		"components. VPN gateway server mode cannot work without it — " +
		"see the Troubleshooting section in the README; the SOCKS5 exit node works without WinNAT")
}

// netNATEntry is one WinNAT instance as reported by
// `Get-NetNat | ConvertTo-Json`.
type netNATEntry struct {
	Name                             string `json:"Name"`
	InternalIPInterfaceAddressPrefix string `json:"InternalIPInterfaceAddressPrefix"`
}

// parseNetNATJSON parses `Get-NetNat | ConvertTo-Json -Compress` output.
// PowerShell emits nothing for an empty result, a single JSON object for one
// instance, and a JSON array for several — all three shapes are handled.
func parseNetNATJSON(data []byte) ([]netNATEntry, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var entries []netNATEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("parse Get-NetNat JSON array: %w", err)
		}
		return entries, nil
	}
	var entry netNATEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("parse Get-NetNat JSON object: %w", err)
	}
	return []netNATEntry{entry}, nil
}

// findNetNAT returns the entry with the given name, if present.
func findNetNAT(entries []netNATEntry, name string) (netNATEntry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return netNATEntry{}, false
}

// listNetNAT returns the current WinNAT instances.
func listNetNAT() ([]netNATEntry, error) {
	out, err := runPowerShell("Get-NetNat | Select-Object Name,InternalIPInterfaceAddressPrefix | ConvertTo-Json -Compress")
	if winNATUnavailable(err) {
		return nil, winNATUnavailableError()
	}
	if err != nil {
		return nil, err
	}
	return parseNetNATJSON(out)
}

// cleanupStaleWinNAT removes a leftover awl-gateway NetNat from a previous
// run that did not get a clean teardown. Returns cleaned=true iff one was
// found (and removed).
func cleanupStaleWinNAT() (bool, error) {
	entries, err := listNetNAT()
	if err != nil {
		return false, err
	}
	if _, found := findNetNAT(entries, winNATName); !found {
		return false, nil
	}
	if err := removeWinNAT(); err != nil {
		return true, err
	}
	return true, nil
}

// createWinNAT creates the awl WinNAT instance. We do not refuse preemptively
// based on Get-NetNat output — HNS-owned instances (Docker on Windows, WSL2)
// are not always visible there and multi-instance setups sometimes work — we
// try, and on failure enrich the error with the current instance list and a
// hint about the usual suspects.
func createWinNAT(awlSubnet string) error {
	cmd := fmt.Sprintf("New-NetNat -Name %s -InternalIPInterfaceAddressPrefix %s | Out-Null", winNATName, awlSubnet)
	_, err := runPowerShell(cmd)
	if err == nil {
		return nil
	}
	if winNATUnavailable(err) {
		return winNATUnavailableError()
	}

	existing := "unavailable"
	if entries, listErr := listNetNAT(); listErr == nil {
		existing = fmt.Sprintf("%+v", entries)
	}
	return fmt.Errorf("create WinNAT instance (WinNAT is effectively single-instance per host; "+
		"existing instances, possibly from Docker/WSL2/ICS, can conflict — current Get-NetNat: %s): %w",
		existing, err)
}

func removeWinNAT() error {
	_, err := runPowerShell(fmt.Sprintf("Remove-NetNat -Name %s -Confirm:$false", winNATName))
	return err
}
