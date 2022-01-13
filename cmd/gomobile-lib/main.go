//go:build linux && android
// +build linux,android

package anywherelan

import (
	"context"
	"os"
	"strconv"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/vpn"
)

var (
	globalApp     *awl.Application
	globalDataDir string
)

// All public functions are part of the library

func InitServer(dataDir string, tunFD int32) error {
	globalDataDir = dataDir
	_ = os.Setenv(config.AppDataDirEnvKey, dataDir)
	_ = os.Setenv(vpn.TunFDEnvKey, strconv.Itoa(int(tunFD)))

	globalApp = awl.New()
	globalApp.SetupLoggerAndConfig()
	err := globalApp.Init(context.Background(), nil)
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
