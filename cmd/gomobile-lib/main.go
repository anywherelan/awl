// +build linux,android

package anywherelan

import (
	"context"
	"os"
	"strconv"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/vpn"
	"github.com/ipfs/go-log/v2"
)

var (
	app           *awl.Application
	logger        *log.ZapEventLogger
	globalDataDir string
)

/*
	export ANDROID_NDK_HOME=$ANDROID_HOME/ndk/20.0.5594570
	gomobile bind -o anywherelan.aar -target=android .
*/

// All public functions are part of a library

// TODO: возвращать ошибку, а не просто логировать
func InitServer(dataDir string, tunFD int32) {
	globalDataDir = dataDir
	_ = os.Setenv(config.AppDataDirEnvKey, dataDir)
	_ = os.Setenv(vpn.TunFDEnvKey, strconv.Itoa(int(tunFD)))

	app = awl.New()
	logger = app.SetupLoggerAndConfig()
	//ctx, ctxCancel := context.WithCancel(context.Background())
	ctx := context.Background()

	err := app.Init(ctx)
	if err != nil {
		logger.Errorf("init server: %v", err)
		app.Close()
	}
}

func StopServer() {
	if app != nil {
		app.Close()
		app = nil
	}
}

func ImportConfig(data string) error {
	if app != nil || globalDataDir == "" {
		panic("call to ImportConfig before server shutdown")
	}

	return config.ImportConfig([]byte(data), globalDataDir)
}

func GetApiAddress() string {
	return app.Api.Address()
}
