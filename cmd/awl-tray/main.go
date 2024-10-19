package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/gen2brain/beeep"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/awldns"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/cli"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/embeds"
	"github.com/anywherelan/awl/update"
)

var (
	app    *awl.Application
	logger *log.ZapEventLogger
)

func getConfig() (*config.Config, error) {
	if app != nil {
		return app.Conf, nil
	}
	return config.LoadConfig(eventbus.NewBus())
}

func main() {
	cli.New(update.AppTypeAwlTray).Run()

	initOSSpecificHacks()

	systray.Run(onReady, onExit)
}

func onReady() {
	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-quitCh
		logger.Infof("received exit signal '%s'", sig)
		systray.Quit()
	}()

	initTray()

	err := InitServer()
	handleErrorWithDialog(err)

	conf, err := getConfig()
	if err != nil {
		logger.Errorf("init awl tray: load config %v", err)
		return
	}
	if conf.Update.TrayAutoCheckEnabled {
		go func() {
			if config.IsDevVersion() {
				logger.Info("updates auto check is disabled for dev version")
				return
			}
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
	_ = embeds.RemoveIconIfNeeded()
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
	// TODO: setup logger in main(), before systray and others
	//  now we can have panics because of this
	logger = app.SetupLoggerAndConfig()

	err = app.Init(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to start server: %v", err)
	}
	app.Api.SetupFrontend(awl.FrontendStatic())

	// Peers counter updater
	app.P2p.SubscribeConnectionEvents(
		func(_ network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			refreshPeersCounterOnPeersConnectionChanged(&peerID)
		},
		func(_ network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			refreshPeersCounterOnPeersConnectionChanged(&peerID)
		},
	)

	subscribeToNotifications(app)
	refreshMenusOnStartedServer()
	refreshPeersCounterOnPeersConnectionChanged(nil)

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
		notifyErr := beeep.Notify(title, "PeerID: \n"+authRequest.PeerID, embeds.GetIconPath())
		if notifyErr != nil {
			logger.Errorf("show notification: incoming friend request: %v", notifyErr)
		}
	}, app.Eventbus, new(awlevent.ReceivedAuthRequest))
}

func openWebGUI(a *awl.Application) error {
	adminURL := "http://" + config.AdminHttpServerDomainName + "." + awldns.LocalDomain
	if checkURL(adminURL) {
		return openURL(adminURL)
	}

	return openURL("http://" + a.Api.Address())
}

func checkURL(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	buf := make([]byte, 8192)
	_, _ = io.ReadFull(resp.Body, buf)
	if !bytes.Contains(buf, []byte("Anywherelan")) {
		return false
	}

	return true
}
