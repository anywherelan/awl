package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/GrigoryKrasnochub/updaterini"
	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/cli"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/update"
	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-eventbus"
	"github.com/ncruces/zenity"
)

var (
	//go:embed Icon.png
	appIcon []byte

	tempIconFilepath = filepath.Join(os.TempDir(), "awl-icon.png")
)

var (
	app    *awl.Application
	logger *log.ZapEventLogger
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

func main() {
	cli.New().Run()

	systray.Run(onReady, onExit)
}

func getConfig() (*config.Config, error) {
	if app != nil {
		return app.Conf, nil
	}
	return config.LoadConfig(eventbus.NewBus())
}

func onReady() {
	_ = os.WriteFile(tempIconFilepath, appIcon, 0666)

	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-quitCh
		logger.Infof("received exit signal '%s'", sig)
		systray.Quit()
	}()

	systray.SetIcon(getIcon())
	systray.SetTitle("Anywherelan")
	systray.SetTooltip("Anywherelan")

	statusMenu = systray.AddMenuItem("", "")
	statusMenu.Disable()

	openBrowserMenu = systray.AddMenuItem("Open Web UI", "")
	go func() {
		for range openBrowserMenu.ClickedCh {
			if a := app; app != nil {
				err := openURL("http://" + a.Api.Address())
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
	err := InitServer()
	handleErrorWithDialog(err)

	conf, err := getConfig()
	if err != nil {
		logger.Errorf("init awl tray: load config %v", err)
		return
	}
	if conf.Update.TrayAutoCheckEnabled {
		go func() {
			interval, err := time.ParseDuration(conf.Update.TrayAutoCheckInterval)
			if err != nil {
				logger.Errorf("update auto check: interval parse: %v", err)
				return
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			checkForUpdatesWithDesktopNotification()
			for range ticker.C {
				checkForUpdatesWithDesktopNotification()
			}
		}()
	}
}

func onExit() {
	StopServer()
	_ = os.Remove(tempIconFilepath)
}

func InitServer() (err error) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			err = fmt.Errorf("recovered panic from starting app: %v", recovered)
		}
		if err != nil && app != nil {
			app.Close()
			app = nil
		}
	}()
	app = awl.New()
	logger = app.SetupLoggerAndConfig()

	err = app.Init(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to start server: %v", err)
	}
	app.Api.SetupFrontend(awl.FrontendStatic())

	subscribeToNotifications(app)
	refreshMenusOnStartedServer()

	return nil
}

func StopServer() {
	defer func() {
		recovered := recover()
		if recovered != nil {
			logger.Errorf("recovered panic from closing app: %v", recovered)
		}
	}()
	defer refreshMenusOnStoppedServer()
	if app != nil {
		app.Close()
	}
	app = nil
}

func subscribeToNotifications(app *awl.Application) {
	awlevent.WrapSubscriptionToCallback(app.Ctx(), func(evt interface{}) {
		authRequest := evt.(awlevent.ReceivedAuthRequest)
		title := "Anywherelan: incoming friend request"
		if authRequest.Name != "" {
			title = fmt.Sprintf("Anywherelan: friend request from %s", authRequest.Name)
		}
		notifyErr := beeep.Notify(title, "PeerID: \n"+authRequest.PeerID, tempIconFilepath)
		if notifyErr != nil {
			logger.Errorf("show notification: incoming friend request: %v", notifyErr)
		}
	}, app.Eventbus, new(awlevent.ReceivedAuthRequest))
}

func getIcon() []byte {
	switch runtime.GOOS {
	case "linux", "darwin":
		return appIcon
	case "windows":
		srcImg, _, err := image.Decode(bytes.NewReader(appIcon))
		if err != nil {
			logger.Errorf("Failed to decode source image: %v", err)
			return appIcon
		}

		destBuf := new(bytes.Buffer)
		err = ico.Encode(destBuf, srcImg)
		if err != nil {
			logger.Errorf("Failed to encode icon: %v", err)
			return appIcon
		}
		return destBuf.Bytes()
	default:
		return appIcon
	}
}

func handleErrorWithDialog(err error) {
	if err == nil {
		return
	}
	logger.Error(err)
	dialogErr := zenity.Error(err.Error(), zenity.Title("Anywherelan error"), zenity.ErrorIcon)
	if dialogErr != nil {
		logger.Errorf("show dialog: error handling: %v", dialogErr)
	}
}

func showInfoDialog(message string, options ...zenity.Option) {
	err := zenity.Info(message, append(options, zenity.InfoIcon)...)
	if err != nil {
		logger.Errorf("show dialog: info: %v", err)
	}
}

func showQuestionDialog(message string, options ...zenity.Option) bool {
	err := zenity.Question(message, append(options, zenity.QuestionIcon)...)
	switch {
	case err == zenity.ErrCanceled:
		return false
	case err != nil:
		logger.Errorf("show dialog: question: %v", err)
	}
	return true
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

func refreshPeersSubmenus() {
	for _, submenu := range peersSubmenus {
		submenu.Hide()
	}
	peersSubmenus = nil

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
		showInfoDialog("App is already up-to-date", zenity.Title("Anywherelan app is up-to-date"), zenity.Width(250))
		return nil
	}

	var serverMessage string
	if app != nil {
		serverMessage = " Server will be stopped!"
	}
	if !showQuestionDialog(fmt.Sprintf("New version available!\nAvailable version %s: %s.\nCurrent version %s.\n\nDo you want to continue?%s",
		updService.NewVersion.VersionTag(), updService.NewVersion.VersionName(), config.Version, serverMessage),
		zenity.Title("Anywherelan new version available"), zenity.OKLabel("Do Update"), zenity.Width(250)) {
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
	if updStatus {
		notifyErr := beeep.Notify("Anywherelan: new version available!",
			fmt.Sprintf("Version %s: %s available for installation!\nUse tray menu option %q\n",
				updService.NewVersion.VersionTag(), updService.NewVersion.VersionName(), updateMenuLabel), tempIconFilepath)
		if notifyErr != nil {
			logger.Errorf("show notification: new version available: %v", notifyErr)
		}
	}
}
