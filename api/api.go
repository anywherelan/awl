package api

import (
	"context"
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
	logBuffer  *ringbuffer.RingBuffer

	echo *echo.Echo
}

func NewHandler(conf *config.Config, p2p *service.P2pService, authStatus *service.AuthStatus,
	logBuffer *ringbuffer.RingBuffer) *Handler {
	return &Handler{
		conf:       conf,
		p2p:        p2p,
		authStatus: authStatus,
		logBuffer:  logBuffer,
	}
}

func (h *Handler) SetupAPI() {
	logger := log.Logger("awl/api")
	h.logger = logger

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
	go func() {
		addr := h.conf.HttpListenAddress
		logger.Infof("starting web server on http://%s", addr)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Warnf("shutting down web server %s: %s", addr, err)
		}
		// TODO ???
		//e.Server.Addr
	}()
}

func (h *Handler) SetupFrontend(fs http.FileSystem) {
	fileServer := http.FileServer(fs)
	h.echo.GET("/*", echo.WrapHandler(fileServer))
}

func (h *Handler) Shutdown(ctx context.Context) error {
	return h.echo.Server.Shutdown(ctx)
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
