package main

import (
	"bytes"
	"context"
	"image"
	"net/http"
	"runtime"

	_ "awl-tray/static"
	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/anywherelan/awl"
	"github.com/getlantern/systray"
	"github.com/ipfs/go-log/v2"
	"github.com/rakyll/statik/fs"
	"github.com/skratchdot/open-golang/open"
)

var (
	statikFS http.FileSystem
	app      *awl.Application
	logger   *log.ZapEventLogger
)

// TODO: переделать на go:embed

//go:generate go run gen.go
// go get github.com/rakyll/statik
//go:generate statik -src ../../static/ -p static

/*
	go build
	GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui
*/

func main() {
	var err error
	statikFS, err = fs.New()
	if err != nil {
		logger.Fatalf("failed to init statik: %v", err)
	}

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
				// TODO: обрабатывать 0.0.0.0 адрес - под windows не работает (заменять на localhost?)
				addr := a.Conf.HttpListenAddress
				open.Run("http://" + addr)
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

	err := app.Init(context.Background())
	if err != nil {
		logger.Errorf("failed to init server: %v", err)
	}
	app.Api.SetupFrontend(statikFS)
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
