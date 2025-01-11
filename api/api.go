package api

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	http_pprof "net/http/pprof"
	"runtime/pprof"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/ipfs/go-log/v2"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/p2p"
	"github.com/anywherelan/awl/ringbuffer"
	"github.com/anywherelan/awl/service"
)

type DNSService interface {
	AwlDNSAddress() string
	IsAwlDNSSetAsSystem() bool
}

type Handler struct {
	conf       *config.Config
	logger     *log.ZapEventLogger
	p2p        *p2p.P2p
	authStatus *service.AuthStatus
	tunnel     *service.Tunnel
	socks5     *service.SOCKS5
	dns        DNSService
	logBuffer  *ringbuffer.RingBuffer

	echo      *echo.Echo
	echoAdmin *echo.Echo

	ctx       context.Context
	ctxCancel context.CancelFunc
}

func NewHandler(conf *config.Config, p2p *p2p.P2p, authStatus *service.AuthStatus, tunnel *service.Tunnel, socks5 *service.SOCKS5,
	logBuffer *ringbuffer.RingBuffer, dns DNSService) *Handler {
	ctx, ctxCancel := context.WithCancel(context.Background())
	return &Handler{
		conf:       conf,
		p2p:        p2p,
		authStatus: authStatus,
		tunnel:     tunnel,
		socks5:     socks5,
		dns:        dns,
		logBuffer:  logBuffer,
		logger:     log.Logger("awl/api"),
		ctx:        ctx,
		ctxCancel:  ctxCancel,
	}
}

func (h *Handler) SetupAPI() error {
	e1, err := h.setupRouter(h.conf.HttpListenAddress)
	if err != nil {
		return err
	}
	h.echo = e1

	if h.conf.HttpListenOnAdminHost {
		echoAdmin, err := h.setupRouter(config.AdminHttpServerListenAddress)
		if err != nil {
			h.logger.Errorf("unable to bind web server on admin host %s: %v", config.AdminHttpServerListenAddress, err)
		} else {
			h.echoAdmin = echoAdmin
		}
	}

	return nil
}

func (h *Handler) setupRouter(address string) (*echo.Echo, error) {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	val := validator.New()
	err := val.RegisterValidation("trimmed_str_not_empty", validateTrimmedStringNotEmpty, false)
	if err != nil {
		return nil, err
	}

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
	e.POST(RemovePeerSettingsPath, h.RemovePeer)
	e.GET(GetAuthRequestsPath, h.GetAuthRequests)
	e.GET(GetBlockedPeersPath, h.GetBlockedPeers)

	// Settings
	e.GET(GetMyPeerInfoPath, h.GetMyPeerInfo)
	e.POST(UpdateMyInfoPath, h.UpdateMySettings)
	e.GET(ListAvailableProxiesPath, h.ListAvailableProxies)
	e.POST(UpdateProxySettingsPath, h.UpdateProxySettings)
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
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("unable to bind address %s: %v", address, err)
	}
	e.Listener = listener
	h.logger.Infof("starting web server on http://%s", listener.Addr().String())
	go func() {
		if err := e.StartServer(e.Server); err != nil && err != http.ErrServerClosed {
			h.logger.Warnf("shutting down web server %s: %v", address, err)
		}
	}()

	return e, nil
}

func (h *Handler) SetupFrontend(fsys fs.FS) {
	fileServer := http.FileServer(http.FS(fsys))
	h.echo.GET("/*", echo.WrapHandler(fileServer))
	if h.echoAdmin != nil {
		h.echoAdmin.GET("/*", echo.WrapHandler(fileServer))
	}
}

func (h *Handler) Shutdown(ctx context.Context) error {
	if h.echoAdmin != nil {
		err := h.echoAdmin.Server.Shutdown(ctx)
		if err != nil {
			h.logger.Errorf("error shutting down web server on admin host %s: %v", config.AdminHttpServerListenAddress, err)
		}
	}

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

func ErrorMessage(message string) Error {
	return Error{Message: message}
}

func validateTrimmedStringNotEmpty(fl validator.FieldLevel) bool {
	str := fl.Field().String()
	str = strings.TrimSpace(str)
	return len(str) > 0
}
