//go:build linux && !android

package netstate

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	"github.com/coreos/go-iptables/iptables"

	"github.com/anywherelan/awl/vpn/privatenet"
)

const (
	awlForwardChain  = "AWL-FORWARD"
	awlForwardChain6 = "AWL6-FORWARD"

	// xtablesLockWaitSec is how long an iptables call waits for the xtables
	// lock. Without a limit, iptables-legacy waits forever while another
	// program holds the lock, and awl hangs with it. iptables-nft has no
	// such lock and ignores the limit.
	xtablesLockWaitSec = 10
)

// natState holds the state needed to teardown NAT rules.
//
// Backend note: rules are created via the system `iptables` binary. On modern
// distros that resolves to iptables-nft; rules created against the legacy
// backend by other software are invisible to it (and vice versa). The library
// used here (coreos/go-iptables) does not bridge that gap.
type natState struct {
	awlSubnet string
	localIP   string // our own address in awlSubnet
	tunIfName string

	// IPv6 NAT state. awlSubnet6 and localIP6 are empty when the IPv6 awl
	// subnet is not configured. ip6tablesOK is set to false when the kernel
	// lacks NAT6 support (missing nf_nat_ipv6 module), so we degrade gracefully
	// to IPv4-only without failing the entire server-side enable.
	awlSubnet6      string
	localIP6        string
	origIPv6Forward string
	ip6tablesOK     bool
}

// newIPTables waits for the xtables lock at most xtablesLockWaitSec. It adds
// PATH to the error when the iptables binary is not found, so a bug report
// shows where the daemon looked for it.
func newIPTables(proto iptables.Protocol) (*iptables.IPTables, error) {
	ipt, err := iptables.New(iptables.IPFamily(proto), iptables.Timeout(xtablesLockWaitSec))
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("%w (PATH=%q)", err, os.Getenv("PATH"))
	}
	return ipt, err
}

// setupNAT enables IP forwarding and configures iptables MASQUERADE for the exit node.
// It uses a dedicated AWL-FORWARD chain so our rules' evaluation order is
// independent of whatever already lives in FORWARD.
//
// If a previous run was killed before teardownNAT could complete, leftover
// state (AWL-FORWARD chain, MASQUERADE rule, ip_forward=1) would otherwise
// cause this function to fail at NewChain. We pre-clean any such leftovers
// best-effort so the new setup gets a clean slate.
//
// awlPrefix and awlPrefix6 are our own address with the prefix length of the
// awl subnet, like 10.66.0.1/11. awlPrefix6 is not valid when IPv6 is off.
func (m *Manager) setupNAT(awlPrefix, awlPrefix6 netip.Prefix, tunIfName string) (*natState, error) {
	if !awlPrefix.IsValid() {
		return nil, errors.New("awl prefix is not set")
	}
	state := &natState{
		awlSubnet: awlPrefix.Masked().String(),
		localIP:   awlPrefix.Addr().String(),
		tunIfName: tunIfName,
	}
	if awlPrefix6.IsValid() {
		state.awlSubnet6 = awlPrefix6.Masked().String()
		state.localIP6 = awlPrefix6.Addr().String()
	}

	ipt, err := newIPTables(iptables.ProtocolIPv4)
	if err != nil {
		return nil, fmt.Errorf("init iptables: %w", err)
	}

	// Pre-clean any leftover scaffolding.
	staleCleaned, err := cleanupStaleNAT(ipt, state)
	if err != nil {
		return nil, fmt.Errorf("pre-clean stale NAT: %w", err)
	}
	if staleCleaned {
		logger.Warnf("recovered from leftover gateway NAT state (previous run was likely killed before teardown)")
	}

	ipForward, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return nil, fmt.Errorf("read ip_forward: %w", err)
	}

	// Turn forwarding on if it is off, and leave it on in teardownNAT. Once it
	// is on, other software may rely on it. Docker turns it on only when it
	// finds it off at its own start: if awl started first, Docker sees 1 and
	// does nothing, and writing 0 back would cut its containers off the network
	// and break their published ports.
	if strings.TrimSpace(string(ipForward)) == "0" {
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
	if state.awlSubnet6 != "" {
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
	ipt6, err := newIPTables(iptables.ProtocolIPv6)
	if err != nil {
		return fmt.Errorf("init ip6tables: %w", err)
	}

	// Pre-clean any stale ip6tables state from a previous run.
	if staleCleaned, err := cleanupStaleNAT6(ipt6, state); err != nil {
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

	// Connections first addressed to our own awl address are not transit
	// through the gateway. The host DNATs them to its own services, like a
	// port that Docker publishes, so they reach FORWARD with a private
	// destination. Return them before the DROPs below and let the host's rules
	// decide, as with the gateway server off. RETURN, not ACCEPT: the host's
	// rules after our jump, like DOCKER-USER, still apply.
	if err := ipt.Append("filter", awlForwardChain, localReturnArgs(state.localIP)...); err != nil {
		return fmt.Errorf("add RETURN rule: %w", err)
	}

	// Lower the MSS of TCP connections through the gateway to what fits the
	// MTU of both interfaces a packet crosses. A peer with a larger TUN MTU
	// than ours announces an MSS that our TUN can't carry back to it, and the
	// download stalls when the ICMP error doesn't reach the server. Before
	// the conntrack ACCEPT below, which would take the server's SYN-ACK
	// first. The gateway works without the rule, so a kernel without the
	// TCPMSS target is not an error.
	if err := ipt.Append("filter", awlForwardChain, mssClampArgs()...); err != nil {
		logger.Warnf("TCP MSS is not clamped on the gateway (connections of peers with a larger MTU may stall): %v", err)
	}

	// conntrack next inside our chain — for two reasons:
	//   - return traffic (dst inside awlSubnet ⊂ 10.0.0.0/8) would otherwise
	//     be dropped by the private-subnet rules below;
	//   - keeps the rule scoped to awl traffic instead of polluting the global
	//     FORWARD chain with a duplicate RELATED,ESTABLISHED ACCEPT.
	if err := ipt.Append("filter", awlForwardChain, conntrackArgs()...); err != nil {
		return fmt.Errorf("add conntrack rule: %w", err)
	}

	// The destinations we refuse to forward, see the privatenet package.
	for _, priv := range privatenet.PrefixesV4() {
		if err := ipt.Append("filter", awlForwardChain, "-d", priv.String(), "-j", "DROP"); err != nil {
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
	// itself — that would NAT peer-to-peer traffic on the mesh interface. Nor
	// the connections to our own address that the host DNATs: the service
	// behind the DNAT sees the peer's awl address, as with the gateway off.
	if err := ipt.Append("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet, state.localIP, state.tunIfName)...); err != nil {
		return fmt.Errorf("add MASQUERADE: %w", err)
	}

	return nil
}

// setupIptables6 mirrors setupIptables for IPv6 using the AWL6-FORWARD chain.
func setupIptables6(ipt6 *iptables.IPTables, state *natState) error {
	if err := ipt6.NewChain("filter", awlForwardChain6); err != nil {
		return fmt.Errorf("create chain %s: %w", awlForwardChain6, err)
	}

	if err := ipt6.Append("filter", awlForwardChain6, localReturnArgs(state.localIP6)...); err != nil {
		return fmt.Errorf("add RETURN rule to %s: %w", awlForwardChain6, err)
	}

	// Optional, see setupIptables.
	if err := ipt6.Append("filter", awlForwardChain6, mssClampArgs()...); err != nil {
		logger.Warnf("IPv6 TCP MSS is not clamped on the gateway (connections of peers with a larger MTU may stall): %v", err)
	}

	if err := ipt6.Append("filter", awlForwardChain6, conntrackArgs()...); err != nil {
		return fmt.Errorf("add conntrack rule to %s: %w", awlForwardChain6, err)
	}

	for _, priv := range privatenet.PrefixesV6() {
		if err := ipt6.Append("filter", awlForwardChain6, "-d", priv.String(), "-j", "DROP"); err != nil {
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

	if err := ipt6.Append("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet6, state.localIP6, state.tunIfName)...); err != nil {
		return fmt.Errorf("add IPv6 MASQUERADE: %w", err)
	}

	return nil
}

// teardownNAT reverses the changes made by setupNAT, except IPv4 forwarding,
// which stays on (see setupNAT). Safe to call on partially set up state.
func (m *Manager) teardownNAT(state *natState) error {
	if state == nil {
		return nil
	}

	errs := teardownIptablesRules(state)

	if state.ip6tablesOK {
		errs = append(errs, teardownIptablesRules6(state)...)
	}

	// Restore IPv6 forwarding if we changed it. Unlike IPv4, leaving it on has
	// a cost: with forwarding on, the kernel ignores router advertisements on
	// interfaces with accept_ra=1, and the host can lose its IPv6 addresses.
	if state.ip6tablesOK && state.origIPv6Forward == "0" {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("0"), 0600); err != nil {
			errs = append(errs, fmt.Errorf("restore ipv6 forwarding: %w", err))
		}
	}

	return errors.Join(errs...)
}

//nolint:dupl // teardownIptablesRules6 mirrors it for IPv6
func teardownIptablesRules(state *natState) []error {
	var errs []error
	ipt, err := newIPTables(iptables.ProtocolIPv4)
	if err != nil {
		return []error{fmt.Errorf("init iptables for teardown: %w", err)}
	}

	if err := ipt.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet, state.localIP, state.tunIfName)...); err != nil {
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

//nolint:dupl // the IPv6 mirror of teardownIptablesRules
func teardownIptablesRules6(state *natState) []error {
	var errs []error
	ipt6, err := newIPTables(iptables.ProtocolIPv6)
	if err != nil {
		return []error{fmt.Errorf("init ip6tables for teardown: %w", err)}
	}

	if err := ipt6.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet6, state.localIP6, state.tunIfName)...); err != nil {
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
func cleanupStaleNAT(ipt *iptables.IPTables, state *natState) (bool, error) {
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

	_ = ipt.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet, state.localIP, state.tunIfName)...)
	_ = ipt.DeleteIfExists("nat", "POSTROUTING", oldMasqueradeArgs(state.awlSubnet, state.tunIfName)...)
	_ = ipt.DeleteIfExists("filter", "FORWARD", returnJumpArgs(state.tunIfName, state.awlSubnet)...)
	_ = ipt.DeleteIfExists("filter", "FORWARD", outboundJumpArgs(state.tunIfName, state.awlSubnet)...)
	_ = ipt.ClearChain("filter", awlForwardChain)
	_ = ipt.DeleteChain("filter", awlForwardChain)

	return true, nil
}

// cleanupStaleNAT6 mirrors cleanupStaleNAT for IPv6. Detection key is the
// AWL6-FORWARD chain.
func cleanupStaleNAT6(ipt6 *iptables.IPTables, state *natState) (bool, error) {
	chainExists, err := ipt6.ChainExists("filter", awlForwardChain6)
	if err != nil {
		return false, fmt.Errorf("check %s chain: %w", awlForwardChain6, err)
	}
	if !chainExists {
		return false, nil
	}

	_ = ipt6.DeleteIfExists("nat", "POSTROUTING", masqueradeArgs(state.awlSubnet6, state.localIP6, state.tunIfName)...)
	_ = ipt6.DeleteIfExists("nat", "POSTROUTING", oldMasqueradeArgs(state.awlSubnet6, state.tunIfName)...)
	_ = ipt6.DeleteIfExists("filter", "FORWARD", returnJumpArgs6(state.tunIfName, state.awlSubnet6)...)
	_ = ipt6.DeleteIfExists("filter", "FORWARD", outboundJumpArgs6(state.tunIfName, state.awlSubnet6)...)
	_ = ipt6.ClearChain("filter", awlForwardChain6)
	_ = ipt6.DeleteChain("filter", awlForwardChain6)

	return true, nil
}

// ─── iptables rule argument builders (IPv4, and shared by both families) ────

// conntrackMatch is the conntrack match with its options.
func conntrackMatch(opts ...string) []string {
	return append([]string{"-m", "conntrack"}, opts...)
}

func conntrackArgs() []string {
	return append(conntrackMatch("--ctstate", "RELATED,ESTABLISHED"), "-j", "ACCEPT")
}

// localReturnArgs matches connections first addressed to localIP, also after
// the host DNATed them. Used for both families.
func localReturnArgs(localIP string) []string {
	return append(conntrackMatch("--ctorigdst", localIP), "-j", "RETURN")
}

// mssClampArgs matches the SYN and SYN-ACK of a TCP handshake and lowers
// their MSS to the path MTU. Used for both families.
func mssClampArgs() []string {
	return []string{"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}
}

func outboundJumpArgs(tunIfName, awlSubnet string) []string {
	return []string{"-i", tunIfName, "-s", awlSubnet, "-j", awlForwardChain}
}

func returnJumpArgs(tunIfName, awlSubnet string) []string {
	return []string{"-o", tunIfName, "-d", awlSubnet, "-j", awlForwardChain}
}

// masqueradeArgs leaves out the connections that localReturnArgs matches. Used
// for both families.
func masqueradeArgs(awlSubnet, localIP, tunIfName string) []string {
	args := append([]string{"-s", awlSubnet, "!", "-o", tunIfName}, conntrackMatch("!", "--ctorigdst", localIP)...)
	return append(args, "-j", "MASQUERADE")
}

// oldMasqueradeArgs is the MASQUERADE rule of awl before v0.21.0, without the
// conntrack condition. cleanupStaleNAT* remove it if an old version left it
// behind. Used for both families.
func oldMasqueradeArgs(awlSubnet, tunIfName string) []string {
	return []string{"-s", awlSubnet, "!", "-o", tunIfName, "-j", "MASQUERADE"}
}

// ─── ip6tables rule argument builders (IPv6) ─────────────────────────────────

func outboundJumpArgs6(tunIfName, awlSubnet6 string) []string {
	return []string{"-i", tunIfName, "-s", awlSubnet6, "-j", awlForwardChain6}
}

func returnJumpArgs6(tunIfName, awlSubnet6 string) []string {
	return []string{"-o", tunIfName, "-d", awlSubnet6, "-j", awlForwardChain6}
}
