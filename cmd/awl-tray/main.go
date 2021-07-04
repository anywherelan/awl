package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image"
	"runtime"

	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/cli"
	"github.com/getlantern/systray"
	"github.com/ipfs/go-log/v2"
	"github.com/ncruces/zenity"
	"github.com/skratchdot/open-golang/open"
)

var (
	//go:embed Icon.png
	appIcon []byte
)

var (
	app    *awl.Application
	logger *log.ZapEventLogger
)

/*
	go build
	GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui
*/

func main() {
	cli.New().Run()

	systray.Run(onReady, onExit)
}

func onReady() {
	handleInitServerError := func(err error) {
		if err == nil {
			return
		}
		logger.Error(err)
		dialogErr := zenity.Error(err.Error(), zenity.Title("Anywherelan error"), zenity.ErrorIcon)
		if dialogErr != nil {
			logger.Errorf("show dialog error: %v", dialogErr)
		}
	}

	systray.SetIcon(getIcon())
	systray.SetTitle("Anywherelan")
	systray.SetTooltip("Anywherelan tray")

	mOpenBrowser := systray.AddMenuItem("Show Web UI", "")
	go func() {
		for range mOpenBrowser.ClickedCh {
			if a := app; app != nil {
				open.Run("http://" + a.Api.Address())
			}
		}
	}()

	mRestart := systray.AddMenuItem("Restart server", "")
	go func() {
		for range mRestart.ClickedCh {
			StopServer()
			err := InitServer()
			handleInitServerError(err)
		}
	}()

	mQuit := systray.AddMenuItem("Quit", "Quit the whole app")
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
		}
	}()

	err := InitServer()
	handleInitServerError(err)
}

func onExit() {
	StopServer()
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

	return nil
}

func StopServer() {
	defer func() {
		recovered := recover()
		if recovered != nil {
			logger.Errorf("recovered panic from closing app: %v", recovered)
		}
	}()
	if app != nil {
		app.Close()
	}
	app = nil
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
