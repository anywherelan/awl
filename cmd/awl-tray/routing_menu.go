package main

import (
	"fmt"
	"sync"

	"fyne.io/systray"
	"github.com/libp2p/go-libp2p/core/peer"
)

// routingPeer is a small adapter type so the unified menu can consume
// entity.AvailableProxy and entity.AvailableVPNGateway with one shape.
type routingPeer struct {
	PeerID    string
	PeerName  string
	Connected bool
}

// routingMenuConfig parameterises routingMenu so the same submenu structure
// powers both the SOCKS5 Proxy and the VPN Gateway entries.
//
// Field semantics:
//   - listPeers — current set of valid candidates (offline peers included; the
//     menu shows them disabled). Order is honored by the UI.
//   - currentSelection — peerID + whether routing through that peer is
//     currently active. For SOCKS5: enabled = (peerID != ""). For VPN Gateway:
//     enabled = Conf.VPNGateway.ClientEnabled.
//   - selectPeer / disable — actions invoked from menu clicks. Errors are
//     surfaced via handleErrorWithDialog; the menu reconciles to the new
//     state on the subsequent refresh().
//   - serveLabel / serveCurrent / serveSet — present only for the VPN gateway
//     menu's "Serve as VPN gateway" toggle. serveLabel == "" disables the
//     toggle entirely (proxy menu has no analogue).
type routingMenuConfig struct {
	noneLabel  string
	emptyLabel string
	serveLabel string

	listPeers        func() []routingPeer
	currentSelection func() (peerID string, enabled bool)
	selectPeer       func(peerID string) error
	disable          func()
	serveCurrent     func() bool
	serveSet         func(bool) error
}

// routingMenu owns one top-level submenu (Proxy or VPN Gateway) and reconciles
// its contents with Application state on each refresh.
//
// Architecture note — fyne/systray on Linux (libdbusmenu/AppIndicator) does not
// survive dynamic Remove(): after Remove() the desktop side keeps rendering the
// old item, clicks on it arrive with an ID that no longer exists, and the click
// is dropped with "systray error: no menu item with ID N" (see fyne-io/systray
// PR #102 / issue #72). To stay clear of it, after the initial build we never
// Remove() anything: static items (info rows, None, serve) are created once,
// and the peer area is an append-only pool of slots. Each slot has a single
// long-lived click goroutine; details on slot/peer mapping live on
// peerClickLoop.
//
// Layout (top-to-bottom inside the submenu):
//
//	Public IP: …          ← info row 1 (Hide() when nothing selected)
//	Ping: …               ← info row 2
//	Via relay: …          ← info row 3
//	_______________
//	None (no proxy / disabled)
//	_______________       ← gateway only
//	Serve as exit node    ← gateway only
//	_______________
//	No proxies available  ← empty-state, Show() iff len(peers)==0
//	peer A                ← peer slots, append-only
//	peer B
//	…
type routingMenu struct {
	root *systray.MenuItem
	cfg  routingMenuConfig

	// mu serialises refresh and click handlers. Click goroutines read
	// slotPeers[idx] under this lock to find out which peer the slot
	// represents at the moment of the click.
	mu sync.Mutex

	infoIPSubmenu    *systray.MenuItem
	infoPingSubmenu  *systray.MenuItem
	infoRelaySubmenu *systray.MenuItem
	infoSeparator    *systray.MenuItem

	// noneSubmenu doubles as the "initialBuild has run" sentinel — until
	// the first refresh() it is nil; after that it (and every other static
	// field above and below) is non-nil for the rest of the menu's life.
	noneSubmenu    *systray.MenuItem
	serveSeparator *systray.MenuItem // nil if cfg.serveLabel == ""
	serveSubmenu   *systray.MenuItem // nil if cfg.serveLabel == ""
	peersSeparator *systray.MenuItem
	// emptySubmenu is shown only when len(peers)==0. Note that hidden peer
	// slots (left over from a previous larger peer set) coexist with it
	// without conflict — they're Hide()d, so the user only sees the
	// "No proxies available" row.
	emptySubmenu *systray.MenuItem

	peerSlots []*systray.MenuItem
	slotPeers []routingPeer // parallel to peerSlots; zero value == slot is hidden
}

func newRoutingMenu(root *systray.MenuItem, cfg routingMenuConfig) *routingMenu {
	return &routingMenu{root: root, cfg: cfg}
}

func (m *routingMenu) refresh() {
	if app == nil || m == nil || m.root == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.noneSubmenu == nil {
		m.initialBuild()
	}

	peers := m.cfg.listPeers()
	selectedPeerID, enabled := m.cfg.currentSelection()

	setChecked(m.noneSubmenu, !enabled)
	if m.serveSubmenu != nil {
		setChecked(m.serveSubmenu, m.cfg.serveCurrent())
	}

	if len(peers) == 0 {
		m.emptySubmenu.Show()
	} else {
		m.emptySubmenu.Hide()
	}

	// Grow the slot pool to cover len(peers). Slots are created once and never
	// removed; spare slots beyond len(peers) are hidden below.
	for len(m.peerSlots) < len(peers) {
		idx := len(m.peerSlots)
		slot := m.root.AddSubMenuItemCheckbox("", "", false)
		m.peerSlots = append(m.peerSlots, slot)
		m.slotPeers = append(m.slotPeers, routingPeer{})
		go m.peerClickLoop(idx, slot)
	}

	for i, slot := range m.peerSlots {
		if i >= len(peers) {
			m.slotPeers[i] = routingPeer{}
			slot.Hide()
			continue
		}
		p := peers[i]
		m.slotPeers[i] = p
		label := p.PeerName
		if !p.Connected {
			label += " (offline)"
		}
		slot.SetTitle(label)
		if p.Connected {
			slot.Enable()
		} else {
			slot.Disable()
		}
		setChecked(slot, enabled && p.PeerID == selectedPeerID)
		slot.Show()
	}

	m.refreshInfoRows(enabled, selectedPeerID)
}

// initialBuild lays out the static items (info rows, separator, None, optional
// serve, empty-state) exactly once. Peer slots are created lazily on demand
// in refresh() — they always live at the bottom of the submenu because the
// systray API has no insert-at-index, only append.
func (m *routingMenu) initialBuild() {
	m.infoIPSubmenu = m.root.AddSubMenuItem("", "")
	m.infoIPSubmenu.Disable()
	m.infoPingSubmenu = m.root.AddSubMenuItem("", "")
	m.infoPingSubmenu.Disable()
	m.infoRelaySubmenu = m.root.AddSubMenuItem("", "")
	m.infoRelaySubmenu.Disable()
	m.infoSeparator = m.root.AddSubMenuItem("_______________", "")
	m.infoSeparator.Disable()

	m.noneSubmenu = m.root.AddSubMenuItemCheckbox(m.cfg.noneLabel, "", false)
	go func() {
		for range m.noneSubmenu.ClickedCh {
			m.cfg.disable()
			m.refresh()
		}
	}()

	if m.cfg.serveLabel != "" {
		m.serveSeparator = m.root.AddSubMenuItem("_______________", "")
		m.serveSeparator.Disable()
		m.serveSubmenu = m.root.AddSubMenuItemCheckbox(m.cfg.serveLabel, "", false)
		go func() {
			for range m.serveSubmenu.ClickedCh {
				next := !m.cfg.serveCurrent()
				if err := m.cfg.serveSet(next); err != nil {
					handleErrorWithDialog(err)
				}
				m.refresh()
			}
		}()
	}

	m.peersSeparator = m.root.AddSubMenuItem("_______________", "")
	m.peersSeparator.Disable()

	m.emptySubmenu = m.root.AddSubMenuItem(m.cfg.emptyLabel, "")
	m.emptySubmenu.Disable()
}

// peerClickLoop is spawned once per slot. The slot index is fixed for the
// lifetime of the goroutine; the peer that the slot represents is resolved
// on each click via slotPeers[idx] under m.mu.
func (m *routingMenu) peerClickLoop(idx int, slot *systray.MenuItem) {
	for range slot.ClickedCh {
		m.mu.Lock()
		p := m.slotPeers[idx]
		m.mu.Unlock()
		if p.PeerID == "" {
			// Slot is hidden — shouldn't happen since libdbusmenu shouldn't
			// dispatch clicks to hidden items, but guard anyway.
			continue
		}
		if err := m.cfg.selectPeer(p.PeerID); err != nil {
			handleErrorWithDialog(err)
		}
		m.refresh()
	}
}

// refreshInfoRows updates the three "Public IP / Ping / Via relay" rows from
// the live p2p layer. The whole section is hidden when no peer is selected;
// when a peer is selected the rows are always visible (with "—" placeholders
// for fields that aren't yet populated) so the user can see that the section
// exists and that data is coming.
func (m *routingMenu) refreshInfoRows(enabled bool, selectedPeerID string) {
	hideAll := func() {
		m.infoIPSubmenu.Hide()
		m.infoPingSubmenu.Hide()
		m.infoRelaySubmenu.Hide()
		m.infoSeparator.Hide()
	}

	if !enabled || selectedPeerID == "" {
		hideAll()
		return
	}
	pid, err := peer.Decode(selectedPeerID)
	if err != nil {
		hideAll()
		return
	}

	publicIP := app.P2p.PeerPublicIP(pid)
	if publicIP == "" {
		publicIP = "unknown"
	}
	m.infoIPSubmenu.SetTitle("Public IP: " + publicIP)
	m.infoIPSubmenu.Show()

	if app.P2p.IsConnected(pid) {
		ping := app.P2p.GetPeerLatency(pid)
		var pingStr string
		if ping > 0 {
			pingStr = fmt.Sprintf("%d ms", ping.Milliseconds())
		} else {
			pingStr = "—"
		}
		m.infoPingSubmenu.SetTitle("Ping: " + pingStr)
		m.infoPingSubmenu.Show()

		via := "no"
		if !app.P2p.HasDirectConnection(pid) {
			via = "yes"
		}
		m.infoRelaySubmenu.SetTitle("Via relay: " + via)
		m.infoRelaySubmenu.Show()
	} else {
		m.infoPingSubmenu.SetTitle("Status: disconnected")
		m.infoPingSubmenu.Show()
		m.infoRelaySubmenu.Hide()
	}
	m.infoSeparator.Show()
}

func setChecked(m *systray.MenuItem, on bool) {
	if on {
		m.Check()
	} else {
		m.Uncheck()
	}
}
