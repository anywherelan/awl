package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"runtime"
	"sort"

	"fyne.io/systray"
	"github.com/GrigoryKrasnochub/updaterini"
	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/gen2brain/beeep"
	"golang.org/x/exp/slices"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/embeds"
	"github.com/anywherelan/awl/update"
)

var (
	statusMenu      *systray.MenuItem
	openBrowserMenu *systray.MenuItem
	peersMenu       *systray.MenuItem
	startStopMenu   *systray.MenuItem
	restartMenu     *systray.MenuItem
	updateMenu      *systray.MenuItem
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

	peersMenu = systray.AddMenuItem("Peers", "Peers")
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
		}
	}()

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
	openBrowserMenu.Enable()
	peersMenu.Enable()
	startStopMenu.SetTitle("Stop server")
	restartMenu.Enable()

	refreshPeersSubmenus()
}

func refreshMenusOnStoppedServer() {
	statusMenu.SetTitle("Status: stopped")
	openBrowserMenu.Disable()
	peersMenu.Disable()
	startStopMenu.SetTitle("Start server")
	restartMenu.Disable()
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
