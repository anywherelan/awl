package anywherelan

import (
	"context"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/config"
	"github.com/ipfs/go-log/v2"
)

var (
	app           *awl.Application
	logger        *log.ZapEventLogger
	globalDataDir string
)

/*
	gomobile bind -o anywherelan.aar -target=android .
*/

// All public functions are part of a library

// TODO: возвращать ошибку, а не просто логировать
func InitServer(dataDir string) {
	globalDataDir = dataDir
	_ = os.Setenv(config.AppDataDirEnvKey, dataDir)

	app = awl.New()
	logger = app.SetupLoggerAndConfig()
	//ctx, ctxCancel := context.WithCancel(context.Background())
	ctx := context.Background()

	err := app.Init(ctx)
	if err != nil {
		logger.Errorf("init server: %v", err)
	}
}

func StopServer() {
	app.Close()
	app = nil
}

func ImportConfig(data string) error {
	if app != nil || globalDataDir == "" {
		panic("call to ImportConfig before server shutdown")
	}

	return config.ImportConfig([]byte(data), globalDataDir)
}

// TODO: переписать попроще, чтобы брать с конца порт (???)
func GetPort() int {
	addr := app.Conf.HttpListenAddress
	return getPortFromAddress(addr)
}

// TODO: ? функция, возвращающая адрес, а не только порт
func getPortFromAddress(addr string) int {
	addr = strings.TrimSpace(addr)
	fields := strings.FieldsFunc(addr, func(r rune) bool {
		return !unicode.IsNumber(r)
	})
	port, _ := strconv.Atoi(fields[len(fields)-1])

	return port
}
