package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/awlevent"
	"github.com/anywherelan/awl/cli"
	"github.com/anywherelan/awl/config"
	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-eventbus"
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
