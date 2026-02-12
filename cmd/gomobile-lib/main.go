//go:build linux && android
// +build linux,android

package anywherelan

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/vpn"
)

var (
	globalApp     *awl.Application
	globalDataDir string
)

// All public functions are part of the library

func Setup(dataDir string) {
	globalDataDir = dataDir
	_ = os.Setenv(config.AppDataDirEnvKey, dataDir)
}

func GetConfig() string {
	if globalDataDir == "" {
		panic("call to GetConfig before Setup")
	}

	conf, loadConfigErr := config.LoadConfig(eventbus.NewBus())
	if loadConfigErr != nil {
		conf = config.NewConfig(eventbus.NewBus())
	}

	data := conf.Export()
	return string(data)
}

func StartServer(tunFD int32) (err error) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			err = fmt.Errorf("recovered panic from InitServer: %v", recovered)
		}
	}()

	_ = os.Setenv(vpn.TunFDEnvKey, strconv.Itoa(int(tunFD)))

	globalApp = awl.New()
	globalApp.SetupLoggerAndConfig()
	err = globalApp.Init(context.Background(), nil)
	if err != nil {
		globalApp.Close()
		globalApp = nil
		return err
	}

	return nil
}

func StopServer() {
	if globalApp != nil {
		globalApp.Close()
		globalApp = nil
	}
}

func ImportConfig(data string) error {
	if globalApp != nil || globalDataDir == "" {
		panic("call to ImportConfig before server shutdown")
	}

	return config.ImportConfig([]byte(data), globalDataDir)
}

func GetApiAddress() string {
	if globalApp != nil && globalApp.Api != nil {
		return globalApp.Api.Address()
	}
	return ""
}
