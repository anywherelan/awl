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

	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/cli"
	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/ipfs/go-log/v2"
	"github.com/ncruces/zenity"
	"github.com/skratchdot/open-golang/open"
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
)

func main() {
	cli.New().Run()

	systray.Run(onReady, onExit)
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
				// TODO: doesn't work on linux under root
				err := open.Run("http://" + a.Api.Address())
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
	mQuit := systray.AddMenuItem("Quit", "Quit the whole app")
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
		}
	}()

	refreshMenusOnStoppedServer()
	err := InitServer()
	handleErrorWithDialog(err)
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
			logger.Errorf("show incoming friend request notification: %v", notifyErr)
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
		logger.Errorf("show dialog error: %v", dialogErr)
	}
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
