//go:build linux && android

package anywherelan

import (
	"context"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/vpn"
	"github.com/anywherelan/awl/vpn/sockmark"
)

var (
	globalApp     *awl.Application
	globalDataDir string
	// globalSwapTUN is the swappable TUN wrapper handed to the Application when
	// a VPN interface is active. UpdateTunDevice swaps a fresh fd into it after
	// the host app re-establishes VpnService with different routes.
	globalSwapTUN *vpn.SwappableTUN
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

// SocketProtector is the interface that the Android host app must implement
// when it wants AWL to mark libp2p sockets so they bypass the VPN. The
// implementation should call android.net.VpnService.protect() under the hood.
//
// The method is named ProtectSocket (rather than Protect) so that the
// generated Java binding does not collide with VpnService.protect — gomobile
// lower-cases the first letter, which would otherwise produce a duplicate
// signature in subclasses.
type SocketProtector interface {
	// ProtectSocket marks the given file descriptor so its traffic bypasses
	// the VPN. Returns true on success.
	ProtectSocket(fd int32) bool
}

// StartServerWithProtector starts the server, registering a socket protector
// so that libp2p and other sockets bypass the VPN. The protector reference is held by
// the Application's sockmark.Marker for the lifetime of the run; calling
// StopServer drops it.
//
// When VPN gateway client mode is enabled in the saved config, the host app
// MUST pass a non-nil protector here, otherwise libp2p traffic would loop
// through the TUN once the host's VpnService.Builder adds 0.0.0.0/0 routes.
func StartServer(tunFD int32, protector SocketProtector) (err error) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			err = fmt.Errorf("recovered panic from InitServer: %v", recovered)
		}
	}()

	globalApp = awl.New()
	globalApp.SetupLoggerAndConfig()
	globalApp.SockMarker = sockmark.NewAndroid(protectorToFunc(protector))

	// A tunFD of 0 means the host did not establish a VPN interface (VPN
	// disabled in config); Init then skips the VPN device entirely. Otherwise
	// wrap the fd in a SwappableTUN so the interface can be replaced at runtime
	// via UpdateTunDevice without restarting P2P.
	var tunDevice tun.Device
	if tunFD > 0 {
		inner, tunErr := vpn.NewAndroidTUNFromFD(int(tunFD))
		if tunErr != nil {
			globalApp = nil
			return tunErr
		}
		globalSwapTUN = vpn.NewSwappableTUN(inner)
		tunDevice = globalSwapTUN
	}

	err = globalApp.Init(context.Background(), tunDevice)
	if err != nil {
		globalApp.Close()
		globalApp = nil
		if globalSwapTUN != nil {
			_ = globalSwapTUN.Close()
			globalSwapTUN = nil
		}
		return err
	}

	return nil
}

// UpdateTunDevice swaps the live TUN interface for the one backed by tunFD,
// which the host app obtained from a fresh VpnService.establish() (e.g. after
// changing routes to toggle VPN gateway mode). P2P connections are unaffected:
// they run on sockets already protected via VpnService.protect, which bypass
// the VPN regardless of the interface, and the swap replaces only the tun fd
// inside the running Application. The previous fd is owned and closed by Go.
func UpdateTunDevice(tunFD int32) error {
	if globalApp == nil {
		return fmt.Errorf("server is not running")
	}
	if globalSwapTUN == nil {
		return fmt.Errorf("vpn interface is not active")
	}

	inner, err := vpn.NewAndroidTUNFromFD(int(tunFD))
	if err != nil {
		return err
	}
	return globalSwapTUN.Swap(inner)
}

func protectorToFunc(p SocketProtector) sockmark.ProtectFunc {
	if p == nil {
		return nil
	}
	return func(fd int) bool {
		return p.ProtectSocket(int32(fd))
	}
}

func StopServer() {
	if globalApp != nil {
		// Application.Close closes the VPN device, which closes globalSwapTUN.
		globalApp.Close()
		globalApp = nil
	}
	globalSwapTUN = nil
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
