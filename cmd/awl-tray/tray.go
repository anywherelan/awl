package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"path/filepath"
	"runtime"
	"slices"
	"sort"

	"fyne.io/systray"
	"github.com/GrigoryKrasnochub/updaterini"
	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/gen2brain/beeep"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/embeds"
	"github.com/anywherelan/awl/service"
	"github.com/anywherelan/awl/update"
)

var (
	statusMenu      *systray.MenuItem
	peersCountMenu  *systray.MenuItem
	openBrowserMenu *systray.MenuItem
	peersMenu       *systray.MenuItem
	proxyMenu       *systray.MenuItem
	gatewayMenu     *systray.MenuItem // nil when service.VPNGatewayClientSupported() != nil
	startStopMenu   *systray.MenuItem
	restartMenu     *systray.MenuItem
	updateMenu      *systray.MenuItem

	proxyRouting   *routingMenu
	gatewayRouting *routingMenu // .root is nil when service.VPNGatewayClientSupported() != nil
)

const updateMenuLabel = "Check for updates"

func getIcon() []byte {
	switch runtime.GOOS {
	case "linux", "darwin":
		return embeds.GetIcon()
	case "windows":
		srcImg, _, err := image.Decode(bytes.NewReader(embeds.GetIcon()))
		if err != nil {
			logger.Errorf("Failed to decode source image: %v", err)
			return embeds.GetIcon()
		}

		destBuf := new(bytes.Buffer)
		err = ico.Encode(destBuf, srcImg)
		if err != nil {
			logger.Errorf("Failed to encode icon: %v", err)
			return embeds.GetIcon()
		}
		return destBuf.Bytes()
	default:
		return embeds.GetIcon()
	}
}

func initTray() {
	systray.SetIcon(getIcon())
	systray.SetTitle("Anywherelan")
	systray.SetTooltip("Anywherelan")

	statusMenu = systray.AddMenuItem("", "")
	statusMenu.Disable()

	peersCountMenu = systray.AddMenuItem("", "")
	peersCountMenu.Disable()

	openBrowserMenu = systray.AddMenuItem("Open Web UI", "")
	go func() {
		for range openBrowserMenu.ClickedCh {
			if a := app; app != nil {
				err := openWebGUI(a)
				if err != nil {
					logger.Errorf("failed to open web ui: %v", err)
				}
			}
		}
	}()
	systray.AddSeparator()

	peersMenu = systray.AddMenuItem("Peers", "")
	go func() {
		// On windows systray does not trigger clicked event on menus with submenus
		for range peersMenu.ClickedCh {
			refreshPeersSubmenus()
		}
	}()
	go func() {
		// Workaround for windows only
		for range systray.TrayOpenedCh {
			if app == nil {
				continue
			}
			refreshPeersSubmenus()
			proxyRouting.refresh()
			gatewayRouting.refresh()
		}
	}()

	proxyMenu = systray.AddMenuItem("Proxy", "")
	proxyRouting = newRoutingMenu(proxyMenu, routingMenuConfig{
		noneLabel:        "None (no proxy)",
		emptyLabel:       `No proxies available — set "Allow as exit node" on a remote device`,
		listPeers:        listProxyPeers,
		currentSelection: currentProxySelection,
		selectPeer: func(peerID string) error {
			app.SOCKS5.SetProxyPeerID(peerID)
			return nil
		},
		disable: func() {
			app.SOCKS5.SetProxyPeerID("")
		},
	})
	go func() {
		// On windows systray does not trigger clicked event on menus with submenus
		for range proxyMenu.ClickedCh {
			proxyRouting.refresh()
		}
	}()

	if service.VPNGatewayClientSupported() == nil {
		gatewayMenu = systray.AddMenuItem("VPN Gateway", "")
		cfg := routingMenuConfig{
			noneLabel:        "None (disabled)",
			emptyLabel:       `No VPN gateways available — set "Allow as exit node" on a remote device`,
			listPeers:        listGatewayPeers,
			currentSelection: currentGatewaySelection,
			selectPeer: func(peerIDStr string) error {
				gatewayPeerID, err := peer.Decode(peerIDStr)
				if err != nil {
					return err
				}
				return app.VPNGateway.EnableClient(gatewayPeerID)
			},
			disable: func() {
				app.VPNGateway.DisableClient()
			},
		}
		// Only expose the "Serve as VPN gateway" toggle on platforms where
		// server mode actually runs. Empty serveLabel makes routingMenu skip
		// the toggle entirely.
		if service.VPNGatewayServerSupported() == nil {
			cfg.serveLabel = "Serve as VPN gateway"
			cfg.serveCurrent = func() bool {
				app.Conf.RLock()
				defer app.Conf.RUnlock()
				return app.Conf.VPNGateway.ServerEnabled
			}
			cfg.serveSet = func(b bool) error {
				return app.VPNGateway.SetServerEnabled(b)
			}
		}
		gatewayRouting = newRoutingMenu(gatewayMenu, cfg)

		go func() {
			// On windows systray does not trigger clicked event on menus with submenus
			for range gatewayMenu.ClickedCh {
				gatewayRouting.refresh()
			}
		}()
	} else {
		// Routing menu code expects a non-nil receiver; give it a no-op stub
		// so refresh()/Enable() calls at runtime are safe and cheap.
		gatewayRouting = newRoutingMenu(nil, routingMenuConfig{})
	}

	startStopMenu = systray.AddMenuItem("", "")
	go func() {
		for range startStopMenu.ClickedCh {
			if app != nil {
				StopServer()
			} else {
				err := InitServer()
				handleErrorWithDialog(err)
			}
		}
	}()

	restartMenu = systray.AddMenuItem("Restart server", "")
	go func() {
		for range restartMenu.ClickedCh {
			StopServer()
			err := InitServer()
			handleErrorWithDialog(err)
		}
	}()

	systray.AddSeparator()
	updateMenu = systray.AddMenuItem(updateMenuLabel, "Check for new version of awl tray")
	go func() {
		for range updateMenu.ClickedCh {
			err := onClickUpdateMenu()
			handleErrorWithDialog(err)
		}
	}()

	advancedMenu := systray.AddMenuItem("Advanced", "")
	editConfigMenu := advancedMenu.AddSubMenuItem("Edit raw config", "Open config file in default editor")
	go func() {
		for range editConfigMenu.ClickedCh {
			onClickEditRawConfig()
		}
	}()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit the whole app")
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
		}
	}()

	refreshMenusOnStoppedServer()
}

func refreshMenusOnStartedServer() {
	statusMenu.SetTitle("Status: running")
	setPeersConnectedCounter(0)
	openBrowserMenu.Enable()
	peersMenu.Enable()
	proxyMenu.Enable()
	if gatewayMenu != nil {
		gatewayMenu.Enable()
	}
	startStopMenu.SetTitle("Stop server")
	restartMenu.Enable()

	refreshPeersSubmenus()
	proxyRouting.refresh()
	gatewayRouting.refresh()
}

func refreshMenusOnStoppedServer() {
	statusMenu.SetTitle("Status: stopped")
	setPeersConnectedCounter(0)
	openBrowserMenu.Disable()
	peersMenu.Disable()
	proxyMenu.Disable()
	if gatewayMenu != nil {
		gatewayMenu.Disable()
	}
	startStopMenu.SetTitle("Start server")
	restartMenu.Disable()
}

// listProxyPeers / currentProxySelection adapt the SOCKS5 service to the
// shape expected by routingMenu.
func listProxyPeers() []routingPeer {
	proxies := app.SOCKS5.ListAvailableProxies()
	out := make([]routingPeer, len(proxies))
	for i, p := range proxies {
		out[i] = routingPeer{PeerID: p.PeerID, PeerName: p.PeerName, Connected: p.Connected}
	}
	return out
}

func currentProxySelection() (string, bool) {
	app.Conf.RLock()
	id := app.Conf.SOCKS5.UsingPeerID
	app.Conf.RUnlock()
	return id, id != ""
}

// listGatewayPeers / currentGatewaySelection adapt the VPN gateway state to
// the shape expected by routingMenu.
func listGatewayPeers() []routingPeer {
	gateways := app.VPNGateway.ListAvailableVPNGateways()
	out := make([]routingPeer, len(gateways))
	for i, g := range gateways {
		out[i] = routingPeer{PeerID: g.PeerID, PeerName: g.PeerName, Connected: g.Connected}
	}
	return out
}

func currentGatewaySelection() (string, bool) {
	app.Conf.RLock()
	defer app.Conf.RUnlock()
	return app.Conf.VPNGateway.GatewayPeerID, app.Conf.VPNGateway.ClientEnabled
}

func setPeersConnectedCounter(peers int) {
	peersCountMenu.SetTitle(fmt.Sprintf("Peers connected: %d", peers))
}

func refreshPeersCounterOnPeersConnectionChanged(peerID *string) {
	if peerID == nil {
		setPeersConnectedCounter(0)
		return
	}
	if _, known := app.Conf.GetPeer(*peerID); !known {
		return
	}

	app.Conf.RLock()
	defer app.Conf.RUnlock()

	connected := 0
	for _, knownPeer := range app.Conf.KnownPeers {
		online := app.P2p.IsConnected(knownPeer.PeerId())
		if online {
			connected++
		}
	}

	setPeersConnectedCounter(connected)
}

var peersSubmenus []*systray.MenuItem
var previousOnlinePeers []string
var previousOfflinePeers []string

// TODO: submenus on linux doesn't work reliably
func refreshPeersSubmenus() {
	app.Conf.RLock()
	onlinePeers := make([]string, 0)
	offlinePeers := make([]string, 0)
	for _, knownPeer := range app.Conf.KnownPeers {
		online := app.P2p.IsConnected(knownPeer.PeerId())
		peerName := knownPeer.DisplayName()
		if peerName == "" {
			peerName = knownPeer.PeerID
		}
		if online {
			onlinePeers = append(onlinePeers, peerName)
		} else {
			offlinePeers = append(offlinePeers, peerName)
		}
	}
	app.Conf.RUnlock()
	sort.Strings(onlinePeers)
	sort.Strings(offlinePeers)

	if slices.Equal(previousOnlinePeers, onlinePeers) && slices.Equal(previousOfflinePeers, offlinePeers) {
		return
	}

	previousOnlinePeers = onlinePeers
	previousOfflinePeers = offlinePeers

	for _, submenu := range peersSubmenus {
		submenu.Remove()
	}
	peersSubmenus = nil

	onlineSubmenu := peersMenu.AddSubMenuItem("Online peers:", "")
	peersSubmenus = append(peersSubmenus, onlineSubmenu)
	for _, peerName := range onlinePeers {
		submenu := peersMenu.AddSubMenuItem(peerName, "")
		submenu.Disable()
		peersSubmenus = append(peersSubmenus, submenu)
	}
	// Workaround due to lack of separators in submenus.
	// https://github.com/getlantern/systray/issues/150
	// https://github.com/getlantern/systray/issues/170
	// TODO: in fyne fork separators should work on linux
	separatorMenu := peersMenu.AddSubMenuItem("_______________", "")
	separatorMenu.Disable()
	peersSubmenus = append(peersSubmenus, separatorMenu)

	offlineSubmenu := peersMenu.AddSubMenuItem("Offline peers:", "")
	peersSubmenus = append(peersSubmenus, offlineSubmenu)
	for _, peerName := range offlinePeers {
		submenu := peersMenu.AddSubMenuItem(peerName, "")
		submenu.Disable()
		peersSubmenus = append(peersSubmenus, submenu)
	}
}

func onClickEditRawConfig() {
	if app != nil {
		ok := showQuestionDialog("Anywherelan", "Server is currently running.\nYou need to stop the server before editing the config,\notherwise your changes will be overwritten.\n\nStop the server now?", "Stop server")
		if !ok {
			return
		}
		StopServer()
	}

	configPath := filepath.Join(config.CalcAppDataDir(), config.AppConfigFilename)
	err := openURL(configPath)
	if err != nil {
		logger.Errorf("failed to open config file %s: %v", configPath, err)
		showErrorDialog("Anywherelan error", fmt.Sprintf("Failed to open config file %s: %v", configPath, err))
	}
}

func onClickUpdateMenu() error {
	updateMenu.SetTitle("Checking...")
	updateMenu.Disable()
	defer func() {
		updateMenu.SetTitle(updateMenuLabel)
		updateMenu.Enable()
	}()
	conf, err := getConfig()
	if err != nil {
		return fmt.Errorf("update: read config: %v", err)
	}
	updService, err := update.NewUpdateService(conf, logger, update.AppTypeAwlTray)
	if err != nil {
		return fmt.Errorf("update: create update service: %v", err)
	}
	updStatus, err := updService.CheckForUpdates()
	if err != nil {
		return fmt.Errorf("update: check for updates: %v", err)
	}
	if !updStatus {
		showInfoDialog("Anywherelan app is up-to-date", "App is already up-to-date")
		return nil
	}

	var serverMessage string
	if app != nil {
		serverMessage = " Server will be stopped!"
	}
	dialogMessage := fmt.Sprintf("New version available!\nAvailable version %s: %s.\nCurrent version %s.\n\nDo you want to continue?%s",
		updService.NewVersion.VersionTag(), updService.NewVersion.VersionName(), config.Version, serverMessage)
	if !showQuestionDialog("Anywherelan new version available", dialogMessage, "Do Update") {
		return nil
	}
	updResult, err := updService.DoUpdate()
	if err != nil {
		return fmt.Errorf("update: updating process: %v", err)
	}
	StopServer()
	return updResult.DeletePreviousVersionFiles(updaterini.DeleteModRerunExec)
}

func checkForUpdatesWithDesktopNotification() {
	conf, err := getConfig()
	if err != nil {
		logger.Errorf("update auto check: load config: %v", err)
		return
	}
	updService, err := update.NewUpdateService(conf, logger, update.AppTypeAwlTray)
	if err != nil {
		logger.Errorf("update auto check: creating update service: %v", err)
		return
	}
	updStatus, err := updService.CheckForUpdates()
	if err != nil {
		logger.Errorf("update auto check: check for updates: %v", err)
		return
	}
	if !updStatus {
		return
	}

	notifyErr := beeep.Notify("Anywherelan: new version available!",
		fmt.Sprintf("Version %s: %s available for installation!\nUse tray menu option %q\n",
			updService.NewVersion.VersionTag(), updService.NewVersion.VersionName(), updateMenuLabel), embeds.GetIconPath())
	if notifyErr != nil {
		logger.Errorf("show notification: new version available: %v", notifyErr)
	}
}
