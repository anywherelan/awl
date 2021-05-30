package main

import (
	"bytes"
	"context"
	_ "embed"
	"image"
	"runtime"

	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/cli"
	"github.com/getlantern/systray"
	"github.com/ipfs/go-log/v2"
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
	InitServer()

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
			InitServer()
		}
	}()

	mQuit := systray.AddMenuItem("Quit", "Quit the whole app")
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
		}
	}()
}

func onExit() {
	StopServer()
}

func InitServer() {
	app = awl.New()
	logger = app.SetupLoggerAndConfig()

	err := app.Init(context.Background(), nil)
	if err != nil {
		logger.Errorf("failed to init server: %v", err)
		app.Close()
		app = nil
		return
	}
	app.Api.SetupFrontend(awl.FrontendStatic())
}

func StopServer() {
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
