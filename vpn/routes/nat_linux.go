//go:build linux && !android

package routes

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/coreos/go-iptables/iptables"
)

const awlForwardChain = "AWL-FORWARD"

// privateSubnets is the destination set we refuse to forward from the gateway,
// so the exit node's LAN, link-local, and CGNAT space stay invisible to
// clients. awlSubnet itself is contained in 10.0.0.0/8 in practice, so
// awl↔awl forward through the gateway is also dropped here — by design:
// peers reach each other directly via libp2p, not via routed IP through an
// exit node.
var privateSubnets = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",  // RFC 6598 — CGNAT
	"169.254.0.0/16", // RFC 3927 — link-local
}

// NATState holds the state needed to teardown NAT rules.
//
// Backend note: rules are created via the system `iptables` binary. On modern
// distros that resolves to iptables-nft; rules created against the legacy
// backend by other software are invisible to it (and vice versa). The library
// used here (coreos/go-iptables) does not bridge that gap.
type NATState struct {
	awlSubnet     string
	tunIfName     string
	origIPForward string
}

// SetupNAT enables IP forwarding and configures iptables MASQUERADE for the exit node.
// It uses a dedicated AWL-FORWARD chain so our rules' evaluation order is
// independent of whatever already lives in FORWARD.
//
// If a previous run was killed before TeardownNAT could complete, leftover
// state (AWL-FORWARD chain, MASQUERADE rule, ip_forward=1) would otherwise
// cause this function to fail at NewChain. We pre-clean any such leftovers
// best-effort so the new setup gets a clean slate.
func SetupNAT(awlSubnet, tunIfName string) (*NATState, error) {
	state := &NATState{
		awlSubnet: awlSubnet,
		tunIfName: tunIfName,
	}

	ipt, err := iptables.New()
	if err != nil {
		return nil, fmt.Errorf("init iptables: %w", err)
	}

	// Pre-clean any leftover scaffolding. We need staleCleaned before
	// deciding whether to trust the captured ip_forward value — see below.
	staleCleaned, err := cleanupStaleNAT(ipt, awlSubnet, tunIfName)
	if err != nil {
		return nil, fmt.Errorf("pre-clean stale NAT: %w", err)
	}
	if staleCleaned {
		logger.Warnf("recovered from leftover gateway NAT state (previous run was likely killed before teardown)")
	}

	origVal, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return nil, fmt.Errorf("read ip_forward: %w", err)
	}
	state.origIPForward = strings.TrimSpace(string(origVal))

	// Only flip forwarding on if it was off. If it was already on we leave it
	// alone and won't touch it on teardown either — many hosts (routers, NAS,
	// k8s nodes, docker bridges) keep ip_forward=1 permanently via sysctl.d,
	// and forcing it back to 0 would silently break them. This also handles
	// stale-recovery: if the previous run died with "1" written, we'll see "1"
	// here and avoid clobbering whatever the user actually wants.
	if state.origIPForward == "0" {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0600); err != nil {
			return nil, fmt.Errorf("enable ip_forward: %w", err)
		}
	}

	// From here on, any failure must invoke TeardownNAT so partial iptables
	// state is rolled back. teardownIptablesRules is idempotent (DeleteIfExists
	// + ChainExists-gated clear/delete), so calling it on a half-built setup is
	// safe.
	if err := setupIptables(ipt, state); err != nil {
		_ = TeardownNAT(state)
		return nil, err
	}

	return state, nil
}

func setupIptables(ipt *iptables.IPTables, state *NATState) error {
	if err := ipt.NewChain("filter", awlForwardChain); err != nil {
		return fmt.Errorf("create chain %s: %w", awlForwardChain, err)
	}

	// conntrack first inside our chain — for two reasons:
	//   - return traffic (dst inside awlSubnet ⊂ 10.0.0.0/8) would otherwise
	//     be dropped by the private-subnet rules below;
	//   - keeps the rule scoped to awl traffic instead of polluting the global
	//     FORWARD chain with a duplicate RELATED,ESTABLISHED ACCEPT.
	if err := ipt.Append("filter", awlForwardChain, conntrackArgs()...); err != nil {
		return fmt.Errorf("add conntrack rule: %w", err)
	}

	for _, priv := range privateSubnets {
		if err := ipt.Append("filter", awlForwardChain, "-d", priv, "-j", "DROP"); err != nil {
			return fmt.Errorf("add DROP rule for %s: %w", priv, err)
		}
	}

	if err := ipt.Append("filter", awlForwardChain, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add ACCEPT rule: %w", err)
	}

	// Two jumps into AWL-FORWARD: one for outbound from awl peers, one for
	// return traffic back to them. Both directions go through the same chain so
	// conntrack inside it covers reply packets without us inserting anything
	// global into FORWARD.
	if err := ipt.Insert("filter", "FORWARD", 1, outboundJumpArgs(state.tunIfName, state.awlSubnet)...); err != nil {
		return fmt.Errorf("insert outbound jump to %s: %w", awlForwardChain, err)
	}
	if err := ipt.Insert("filter", "FORWARD", 1, returnJumpArgs(state.tunIfName, state.awlSubnet)...); err != nil {
		return fmt.Errorf("insert return jump to %s: %w", awlForwardChain, err)
	}

	// MASQUERADE outgoing traffic from awl subnet, but never on the TUN
	// itself — that would NAT peer-to-peer traffic on the mesh interface.
	if err := ipt.Append("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet, state.tunIfName)...); err != nil {
		return fmt.Errorf("add MASQUERADE: %w", err)
	}

	return nil
}

// TeardownNAT reverses the changes made by SetupNAT. Safe to call on partially
// set up state.
func TeardownNAT(state *NATState) error {
	if state == nil {
		return nil
	}

	errs := teardownIptablesRules(state)

	// Mirror of SetupNAT: we only enabled forwarding if it was off, so we only
	// restore in that case. If it was already on, leave the kernel value alone.
	if state.origIPForward == "0" {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("0"), 0600); err != nil {
			errs = append(errs, fmt.Errorf("restore ip_forward: %w", err))
		}
	}

	return errors.Join(errs...)
}

func teardownIptablesRules(state *NATState) []error {
	var errs []error
	ipt, err := iptables.New()
	if err != nil {
		return []error{fmt.Errorf("init iptables for teardown: %w", err)}
	}

	if err := ipt.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet, state.tunIfName)...); err != nil {
		errs = append(errs, fmt.Errorf("del MASQUERADE: %w", err))
	}
	if err := ipt.DeleteIfExists("filter", "FORWARD", returnJumpArgs(state.tunIfName, state.awlSubnet)...); err != nil {
		errs = append(errs, fmt.Errorf("del return jump: %w", err))
	}
	if err := ipt.DeleteIfExists("filter", "FORWARD", outboundJumpArgs(state.tunIfName, state.awlSubnet)...); err != nil {
		errs = append(errs, fmt.Errorf("del outbound jump: %w", err))
	}

	exists, err := ipt.ChainExists("filter", awlForwardChain)
	if err != nil {
		errs = append(errs, fmt.Errorf("check %s chain: %w", awlForwardChain, err))
		return errs
	}
	if exists {
		if err := ipt.ClearChain("filter", awlForwardChain); err != nil {
			errs = append(errs, fmt.Errorf("flush chain %s: %w", awlForwardChain, err))
		}
		if err := ipt.DeleteChain("filter", awlForwardChain); err != nil {
			errs = append(errs, fmt.Errorf("del chain %s: %w", awlForwardChain, err))
		}
	}
	return errs
}

// cleanupStaleNAT removes leftover NAT state from a previous SetupNAT call
// that did not get a clean teardown (kill -9, OOM, etc). Detection key is the
// presence of the AWL-FORWARD chain — if it exists, we assume the rest of the
// awl NAT scaffolding may also be present and try to remove it. All operations
// are *IfExists / clear-then-delete so callers get an idempotent best-effort
// pre-clean.
//
// Returns (cleaned, err) where cleaned is true iff a stale chain was detected
// (and thus removed). err is only returned for unexpected ChainExists failures;
// the per-operation deletes' errors are intentionally swallowed because the
// goal is "make NewChain succeed", not "perfectly mirror teardown".
func cleanupStaleNAT(ipt *iptables.IPTables, awlSubnet, tunIfName string) (bool, error) {
	chainExists, err := ipt.ChainExists("filter", awlForwardChain)
	if err != nil {
		return false, fmt.Errorf("check %s chain: %w", awlForwardChain, err)
	}
	if !chainExists {
		// No leftover scaffolding. A bare MASQUERADE without the chain would
		// be very surprising; we don't speculatively delete it so as not to
		// touch user state.
		return false, nil
	}

	_ = ipt.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs(awlSubnet, tunIfName)...)
	_ = ipt.DeleteIfExists("filter", "FORWARD", returnJumpArgs(tunIfName, awlSubnet)...)
	_ = ipt.DeleteIfExists("filter", "FORWARD", outboundJumpArgs(tunIfName, awlSubnet)...)
	_ = ipt.ClearChain("filter", awlForwardChain)
	_ = ipt.DeleteChain("filter", awlForwardChain)

	return true, nil
}

func conntrackArgs() []string {
	return []string{"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}
}

func outboundJumpArgs(tunIfName, awlSubnet string) []string {
	return []string{"-i", tunIfName, "-s", awlSubnet, "-j", awlForwardChain}
}

func returnJumpArgs(tunIfName, awlSubnet string) []string {
	return []string{"-o", tunIfName, "-d", awlSubnet, "-j", awlForwardChain}
}

func masqueradeArgs(awlSubnet, tunIfName string) []string {
	return []string{"-s", awlSubnet, "!", "-o", tunIfName, "-j", "MASQUERADE"}
}
