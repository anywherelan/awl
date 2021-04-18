package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	http_pprof "net/http/pprof"
	"runtime/pprof"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/ringbuffer"
	"github.com/anywherelan/awl/service"
	"github.com/go-playground/validator/v10"
	"github.com/ipfs/go-log/v2"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Handler struct {
	conf       *config.Config
	logger     *log.ZapEventLogger
	p2p        *service.P2pService
	authStatus *service.AuthStatus
	tunnel     *service.Tunnel
	logBuffer  *ringbuffer.RingBuffer

	echo *echo.Echo
}

func NewHandler(conf *config.Config, p2p *service.P2pService, authStatus *service.AuthStatus,
	tunnel *service.Tunnel, logBuffer *ringbuffer.RingBuffer) *Handler {
	return &Handler{
		conf:       conf,
		p2p:        p2p,
		authStatus: authStatus,
		tunnel:     tunnel,
		logBuffer:  logBuffer,
		logger:     log.Logger("awl/api"),
	}
}

func (h *Handler) SetupAPI() error {
	e := echo.New()
	h.echo = e
	e.HideBanner = true
	e.HidePort = true
	val := validator.New()
	e.Validator = &customValidator{validator: val}

	// Middleware
	if !h.conf.DevMode() {
		e.Use(middleware.Recover())
	}

	// Routes

	// Peers
	e.GET(GetKnownPeersPath, h.GetKnownPeers)
	e.POST(GetKnownPeerSettingsPath, h.GetKnownPeerSettings)
	e.POST(SendFriendRequestPath, h.SendFriendRequest)
	e.POST(AcceptPeerInvitationPath, h.AcceptFriend)
	e.POST(UpdatePeerSettingsPath, h.UpdatePeerSettings)
	e.GET(GetAuthRequestsPath, h.GetAuthRequests)

	// Settings
	e.GET(GetMyPeerInfoPath, h.GetMyPeerInfo)
	e.POST(UpdateMyInfoPath, h.UpdateMySettings)
	e.GET(ExportServerConfigPath, h.ExportServerConfiguration)

	// Debug
	e.GET(GetP2pDebugInfoPath, h.GetP2pDebugInfo)
	e.GET(GetDebugLogPath, h.GetLog)

	if h.conf.DevMode() {
		e.Any(V0Prefix+"debug/pprof/", echo.WrapHandler(http.HandlerFunc(http_pprof.Index)))
		e.Any(V0Prefix+"debug/pprof/profile", echo.WrapHandler(http.HandlerFunc(http_pprof.Profile)))
		e.Any(V0Prefix+"debug/pprof/trace", echo.WrapHandler(http.HandlerFunc(http_pprof.Trace)))
		e.Any(V0Prefix+"debug/pprof/cmdline", echo.WrapHandler(http.HandlerFunc(http_pprof.Cmdline)))
		e.Any(V0Prefix+"debug/pprof/symbol", echo.WrapHandler(http.HandlerFunc(http_pprof.Symbol)))

		for _, p := range pprof.Profiles() {
			name := p.Name()
			e.Any(V0Prefix+"debug/pprof/"+name, echo.WrapHandler(http_pprof.Handler(name)))
		}
	}

	// Start
	address := h.conf.HttpListenAddress
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("unable to bind address %s: %v", address, err)
	}
	h.echo.Listener = listener
	h.logger.Infof("starting web server on http://%s", listener.Addr().String())
	go func() {
		if err := e.StartServer(e.Server); err != nil && err != http.ErrServerClosed {
			h.logger.Warnf("shutting down web server %s: %s", address, err)
		}
	}()

	return nil
}

func (h *Handler) SetupFrontend(fs http.FileSystem) {
	fileServer := http.FileServer(fs)
	h.echo.GET("/*", echo.WrapHandler(fileServer))
}

func (h *Handler) Shutdown(ctx context.Context) error {
	return h.echo.Server.Shutdown(ctx)
}

func (h *Handler) Address() string {
	address := h.echo.Listener.Addr().String()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		panic(err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return address
	} else if ip.IsUnspecified() {
		return net.JoinHostPort("127.0.0.1", port)
	}

	return net.JoinHostPort(ip.String(), port)
}

type customValidator struct {
	validator *validator.Validate
}

func (cv *customValidator) Validate(i interface{}) error {
	return cv.validator.Struct(i)
}

type Error struct {
	Message string `json:"error"`
}

func (e Error) Error() string {
	return e.Message
}

func InternalError() Error {
	return Error{Message: "Internal Server Error"}
}

func ErrorMessage(message string) Error {
	return Error{Message: message}
}
