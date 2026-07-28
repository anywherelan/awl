//go:build linux && !android

package netstate

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/coreos/go-iptables/iptables"
)

const (
	awlForwardChain  = "AWL-FORWARD"
	awlForwardChain6 = "AWL6-FORWARD"
)

// privateSubnets (the destination set we refuse to forward) is shared across
// platforms — see private_subnets.go.

// natState holds the state needed to teardown NAT rules.
//
// Backend note: rules are created via the system `iptables` binary. On modern
// distros that resolves to iptables-nft; rules created against the legacy
// backend by other software are invisible to it (and vice versa). The library
// used here (coreos/go-iptables) does not bridge that gap.
type natState struct {
	awlSubnet     string
	tunIfName     string
	origIPForward string

	// IPv6 NAT state. awlSubnet6 is empty when the IPv6 awl subnet is not
	// configured. ip6tablesOK is set to false when the kernel lacks NAT6
	// support (missing nf_nat_ipv6 module), so we degrade gracefully to
	// IPv4-only without failing the entire server-side enable.
	awlSubnet6      string
	origIPv6Forward string
	ip6tablesOK     bool
}

// setupNAT enables IP forwarding and configures iptables MASQUERADE for the exit node.
// It uses a dedicated AWL-FORWARD chain so our rules' evaluation order is
// independent of whatever already lives in FORWARD.
//
// If a previous run was killed before teardownNAT could complete, leftover
// state (AWL-FORWARD chain, MASQUERADE rule, ip_forward=1) would otherwise
// cause this function to fail at NewChain. We pre-clean any such leftovers
// best-effort so the new setup gets a clean slate.
func (m *Manager) setupNAT(awlSubnet, awlSubnet6, tunIfName string) (*natState, error) {
	state := &natState{
		awlSubnet:  awlSubnet,
		awlSubnet6: awlSubnet6,
		tunIfName:  tunIfName,
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

	// From here on, any failure must invoke teardownNAT so partial iptables
	// state is rolled back. teardownIptablesRules is idempotent (DeleteIfExists
	// + ChainExists-gated clear/delete), so calling it on a half-built setup is
	// safe.
	if err := setupIptables(ipt, state); err != nil {
		_ = m.teardownNAT(state)
		return nil, err
	}

	// IPv6 NAT — optional, degrades gracefully when the kernel has no NAT6
	// support (nf_nat_ipv6 module missing, common on minimised kernels).
	if awlSubnet6 != "" {
		if err := m.setupNATv6(state); err != nil {
			// Log and continue: IPv4 gateway still works.
			logger.Warnf("IPv6 NAT setup failed (IPv4 gateway still active, IPv6 will not be tunnelled): %v", err)
		}
	}

	return state, nil
}

// setupNATv6 configures ip6tables MASQUERADE for the IPv6 awl subnet.
// It mirrors setupIptables but for IPv6. Errors are not fatal to the overall
// NAT setup — they are logged and the gateway continues with IPv4 only.
func (m *Manager) setupNATv6(state *natState) error {
	// Enable IPv6 forwarding (mirrors ip_forward logic above).
	origVal, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
	if err != nil {
		return fmt.Errorf("read ipv6 forwarding: %w", err)
	}
	state.origIPv6Forward = strings.TrimSpace(string(origVal))
	if state.origIPv6Forward == "0" {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0600); err != nil {
			return fmt.Errorf("enable ipv6 forwarding: %w", err)
		}
	}

	// ip6tables with nat table — will fail if the kernel module is absent.
	ipt6, err := iptables.NewWithProtocol(iptables.ProtocolIPv6)
	if err != nil {
		return fmt.Errorf("init ip6tables: %w", err)
	}

	// Pre-clean any stale ip6tables state from a previous run.
	if staleCleaned, err := cleanupStaleNAT6(ipt6, state.awlSubnet6, state.tunIfName); err != nil {
		return fmt.Errorf("pre-clean stale NAT6: %w", err)
	} else if staleCleaned {
		logger.Warnf("recovered from leftover gateway NAT6 state")
	}

	if err := setupIptables6(ipt6, state); err != nil {
		// Roll back ip6tables partial state, but leave ipv6 forwarding as-is
		// (same hands-off policy as IPv4: if it was already on, keep it).
		_ = teardownIptablesRules6(state)
		return err
	}

	state.ip6tablesOK = true
	logger.Infof("IPv6 NAT (MASQUERADE) configured for subnet %s on %s", state.awlSubnet6, state.tunIfName)
	return nil
}

func setupIptables(ipt *iptables.IPTables, state *natState) error {
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

// setupIptables6 mirrors setupIptables for IPv6 using the AWL6-FORWARD chain.
func setupIptables6(ipt6 *iptables.IPTables, state *natState) error {
	if err := ipt6.NewChain("filter", awlForwardChain6); err != nil {
		return fmt.Errorf("create chain %s: %w", awlForwardChain6, err)
	}

	if err := ipt6.Append("filter", awlForwardChain6, conntrackArgs()...); err != nil {
		return fmt.Errorf("add conntrack rule to %s: %w", awlForwardChain6, err)
	}

	for _, priv := range privateSubnetsV6 {
		if err := ipt6.Append("filter", awlForwardChain6, "-d", priv, "-j", "DROP"); err != nil {
			return fmt.Errorf("add IPv6 DROP rule for %s to %s: %w", priv, awlForwardChain6, err)
		}
	}

	if err := ipt6.Append("filter", awlForwardChain6, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("add ACCEPT rule to %s: %w", awlForwardChain6, err)
	}

	if err := ipt6.Insert("filter", "FORWARD", 1, outboundJumpArgs6(state.tunIfName, state.awlSubnet6)...); err != nil {
		return fmt.Errorf("insert outbound jump to %s: %w", awlForwardChain6, err)
	}
	if err := ipt6.Insert("filter", "FORWARD", 1, returnJumpArgs6(state.tunIfName, state.awlSubnet6)...); err != nil {
		return fmt.Errorf("insert return jump to %s: %w", awlForwardChain6, err)
	}

	if err := ipt6.Append("nat", "POSTROUTING", masqueradeArgs6(state.awlSubnet6, state.tunIfName)...); err != nil {
		return fmt.Errorf("add IPv6 MASQUERADE: %w", err)
	}

	return nil
}

// teardownNAT reverses the changes made by setupNAT. Safe to call on partially
// set up state.
func (m *Manager) teardownNAT(state *natState) error {
	if state == nil {
		return nil
	}

	errs := teardownIptablesRules(state)

	if state.ip6tablesOK {
		errs = append(errs, teardownIptablesRules6(state)...)
	}

	// Restore IPv4 forwarding if we changed it.
	if state.origIPForward == "0" {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("0"), 0600); err != nil {
			errs = append(errs, fmt.Errorf("restore ip_forward: %w", err))
		}
	}

	// Restore IPv6 forwarding if we changed it.
	if state.ip6tablesOK && state.origIPv6Forward == "0" {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("0"), 0600); err != nil {
			errs = append(errs, fmt.Errorf("restore ipv6 forwarding: %w", err))
		}
	}

	return errors.Join(errs...)
}

func teardownIptablesRules(state *natState) []error {
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

func teardownIptablesRules6(state *natState) []error {
	var errs []error
	ipt6, err := iptables.NewWithProtocol(iptables.ProtocolIPv6)
	if err != nil {
		return []error{fmt.Errorf("init ip6tables for teardown: %w", err)}
	}

	if err := ipt6.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs6(state.awlSubnet6, state.tunIfName)...); err != nil {
		errs = append(errs, fmt.Errorf("del IPv6 MASQUERADE: %w", err))
	}
	if err := ipt6.DeleteIfExists("filter", "FORWARD", returnJumpArgs6(state.tunIfName, state.awlSubnet6)...); err != nil {
		errs = append(errs, fmt.Errorf("del IPv6 return jump: %w", err))
	}
	if err := ipt6.DeleteIfExists("filter", "FORWARD", outboundJumpArgs6(state.tunIfName, state.awlSubnet6)...); err != nil {
		errs = append(errs, fmt.Errorf("del IPv6 outbound jump: %w", err))
	}

	exists, err := ipt6.ChainExists("filter", awlForwardChain6)
	if err != nil {
		errs = append(errs, fmt.Errorf("check %s chain: %w", awlForwardChain6, err))
		return errs
	}
	if exists {
		if err := ipt6.ClearChain("filter", awlForwardChain6); err != nil {
			errs = append(errs, fmt.Errorf("flush chain %s: %w", awlForwardChain6, err))
		}
		if err := ipt6.DeleteChain("filter", awlForwardChain6); err != nil {
			errs = append(errs, fmt.Errorf("del chain %s: %w", awlForwardChain6, err))
		}
	}
	return errs
}

// cleanupStaleNAT removes leftover NAT state from a previous setupNAT call
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

// cleanupStaleNAT6 mirrors cleanupStaleNAT for IPv6. Detection key is the
// AWL6-FORWARD chain.
func cleanupStaleNAT6(ipt6 *iptables.IPTables, awlSubnet6, tunIfName string) (bool, error) {
	chainExists, err := ipt6.ChainExists("filter", awlForwardChain6)
	if err != nil {
		return false, fmt.Errorf("check %s chain: %w", awlForwardChain6, err)
	}
	if !chainExists {
		return false, nil
	}

	_ = ipt6.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs6(awlSubnet6, tunIfName)...)
	_ = ipt6.DeleteIfExists("filter", "FORWARD", returnJumpArgs6(tunIfName, awlSubnet6)...)
	_ = ipt6.DeleteIfExists("filter", "FORWARD", outboundJumpArgs6(tunIfName, awlSubnet6)...)
	_ = ipt6.ClearChain("filter", awlForwardChain6)
	_ = ipt6.DeleteChain("filter", awlForwardChain6)

	return true, nil
}

// ─── iptables rule argument builders (IPv4) ─────────────────────────────────

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

// ─── ip6tables rule argument builders (IPv6) ─────────────────────────────────

func outboundJumpArgs6(tunIfName, awlSubnet6 string) []string {
	return []string{"-i", tunIfName, "-s", awlSubnet6, "-j", awlForwardChain6}
}

func returnJumpArgs6(tunIfName, awlSubnet6 string) []string {
	return []string{"-o", tunIfName, "-d", awlSubnet6, "-j", awlForwardChain6}
}

func masqueradeArgs6(awlSubnet6, tunIfName string) []string {
	return []string{"-s", awlSubnet6, "!", "-o", tunIfName, "-j", "MASQUERADE"}
}
