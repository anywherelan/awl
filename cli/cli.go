package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	"github.com/GrigoryKrasnochub/updaterini"
	"github.com/ipfs/go-log/v2"
	"github.com/urfave/cli/v3"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/update"
)

const (
	WithEnvCommandName = "with-env"
	CliCommandName     = "cli"
)

const (
	flagPeerID   = "pid"
	flagPeerName = "name"
)

var defaultApiAddr = "127.0.0.1:" + strconv.Itoa(config.DefaultHTTPPort)

var binaryName = path.Base(os.Args[0])

type Application struct {
	logger  *log.ZapEventLogger
	api     *apiclient.Client
	cliapp  *cli.Command
	appType config.AppType
}

func New(appType config.AppType) *Application {
	app := new(Application)
	app.logger = log.Logger("awl/cli")
	app.appType = appType
	app.init()

	return app
}

// RunWithWriter runs CLI commands with the given args, writing output to w.
// Suitable for use in tests. Unlike Run(), it does not read os.Args or call os.Exit.
func (a *Application) RunWithWriter(args []string, w io.Writer) error {
	a.cliapp.Writer = w
	return a.cliapp.Run(context.Background(), args)
}

func (a *Application) Run() {
	if len(os.Args) == 1 {
		return
	}
	// macOS passes -psn_X_Y (process serial number) to an app bundle on the first launch of a quarantined app
	if runtime.GOOS == "darwin" && strings.HasPrefix(os.Args[1], "-psn_") {
		return
	}

	switch arg := os.Args[1]; arg {
	case WithEnvCommandName:
		// is handled in linux_root_hacks.go
		return
	case CliCommandName:
		err := a.cliapp.Run(context.Background(), os.Args[1:])
		if err != nil {
			a.logger.Fatalf("Error occurred: %v", err)
		}
		os.Exit(0)
	case "-h", "--help":
		a.printGlobalHelp()
		os.Exit(0)
	case "-v", "--version":
		a.printVersion()
		os.Exit(0)
	default:
		fmt.Printf("Unknown command '%s'. Use '%s -h' for help, or run '%s' to start %s\n", arg, binaryName, binaryName, a.startsWithoutCommand())
		os.Exit(-1)
	}
}

func (a *Application) init() {
	a.cliapp = &cli.Command{
		Name:                          binaryName + " " + CliCommandName,
		Description:                   cliDescription,
		CustomRootCommandHelpTemplate: cli.RootCommandHelpTemplate + cliHelpFooter,
		Version:                       config.Version,
		Usage:                         projectTagline,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "api_addr",
				Usage:    fmt.Sprintf("awl api address, example: %s", defaultApiAddr),
				Required: false,
			},
			&cli.StringFlag{
				Name:     "api_user",
				Usage:    "username for api basic auth",
				Required: false,
			},
			&cli.StringFlag{
				Name:     "api_password",
				Usage:    "password for api basic auth",
				Required: false,
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "me",
				Usage: "Manage this device: status, peer id, name, SOCKS5 proxy",
				Commands: []*cli.Command{
					{
						Name:  "status",
						Usage: "Print your server status and network stats",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return printStatus(a.api, cmd.Writer)
						},
					},
					{
						Name:  "id",
						Usage: "Print your peer id, with a link and a QR code to share it",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return printPeerId(a.api, cmd.Writer)
						},
					},
					{
						Name:  "rename",
						Usage: "Rename your peer",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: true,
							},
						},
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return renameMe(a.api, cmd.String("name"), cmd.Writer)
						},
					},
					{
						Name:  "list_proxies",
						Usage: "List SOCKS5 proxies available to you",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return listProxies(a.api, cmd.Writer)
						},
					},
					{
						Name:  "set_proxy",
						Usage: "Route local SOCKS5 traffic through a peer; empty pid and name stop it",
						Flags: peerSelectorFlags(),
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiAndPeerId(cmd, false)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return setProxy(a.api, cmd.String(flagPeerID), cmd.Writer)
						},
					},
				},
			},
			{
				Name:  "peers",
				Usage: "Manage known peers and incoming friend requests",
				Commands: []*cli.Command{
					{
						Name:  "status",
						Usage: "Print known peers and their status",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "format",
								Aliases:  []string{"f"},
								Required: false,
								Value:    "npslucev",
								Usage: "control table columns list and order.Each char add column, write column chars together without gap. Use these chars to add specific columns:\n   " +
									"n - peers number\n   p - peers name, domain and ip address\n   i - peers id\n   s - peers status\n   l - peers last seen datetime\n   v - peers awl version" +
									"\n   u - network usage by peer (in/out)\n   c - list of peers connections (IP address + protocol)\n   e - exit node status\n  ",
							},
						},
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return printPeersStatus(a.api, cmd.String("format"), cmd.Writer)
						},
					},
					{
						Name:  "requests",
						Usage: "Print incoming friend requests",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return printFriendRequests(a.api, cmd.Writer)
						},
					},
					{
						Name:  "add",
						Usage: "Invite a peer, or accept its existing invitation",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "link",
								Usage:    "invite link to add the peer by, instead of pid: awl://invite?p=...",
								Required: false,
							},
							&cli.StringFlag{
								Name:     flagPeerID,
								Usage:    "peer id",
								Required: false,
							},
							&cli.StringFlag{
								Name:     flagPeerName,
								Usage:    "peer name, defaults to the name from the link or from the peer's incoming request. Without link and pid, selects the incoming request by this name",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "ip",
								Usage:    "peer IP address. By default the device gets the address it announces, once it accepts the friend request",
								Required: false,
							},
							&cli.BoolFlag{
								Name:     "allow-exit-node",
								Usage:    "allow this peer to use your device as an exit node (VPN gateway or SOCKS5 proxy)",
								Required: false,
							},
						},
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return addPeer(a.api, addPeerParams{
								Link:                 cmd.String("link"),
								PeerID:               cmd.String(flagPeerID),
								Alias:                cmd.String(flagPeerName),
								IPAddr:               cmd.String("ip"),
								AllowUsingAsExitNode: cmd.Bool("allow-exit-node"),
							}, cmd.Writer)
						},
					},
					{
						Name:  "decline",
						Usage: "Decline an incoming friend request and block the peer. The peer sees its request as declined",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     flagPeerID,
								Usage:    "peer id, as printed by 'peers requests'",
								Required: false,
							},
							&cli.StringFlag{
								Name:     flagPeerName,
								Usage:    "peer name, as printed by 'peers requests'",
								Required: false,
							},
						},
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return declinePeer(a.api, cmd.String(flagPeerID), cmd.String(flagPeerName), cmd.Writer)
						},
					},
					{
						Name:  "remove",
						Usage: "Remove a peer from the friends list and block it until you add it again",
						Flags: peerSelectorFlags(),
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiAndPeerIdRequired(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return removePeer(a.api, cmd.String(flagPeerID), cmd.Writer)
						},
					},
					{
						Name:  "blocked",
						Usage: "Print blocked peers: the ones you declined or removed",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return printBlockedPeers(a.api, cmd.Writer)
						},
					},
					{
						Name:  "unblock",
						Usage: "Unblock a peer so it can send you friend requests again; this does not add it to your peers",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     flagPeerID,
								Usage:    "peer id, as printed by 'peers blocked'",
								Required: false,
							},
							&cli.StringFlag{
								Name:     flagPeerName,
								Usage:    "peer name, as printed by 'peers blocked'",
								Required: false,
							},
						},
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return unblockPeer(a.api, cmd.String(flagPeerID), cmd.String(flagPeerName), cmd.Writer)
						},
					},
					{
						Name:  "rename",
						Usage: "Rename a known peer",
						Flags: peerSelectorFlags(
							&cli.StringFlag{
								Name:     "new_name",
								Usage:    "peer new name",
								Required: true,
							},
						),
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiAndPeerIdRequired(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return changePeerAlias(a.api, cmd.String(flagPeerID), cmd.String("new_name"), cmd.Writer)
						},
					},
					{
						Name:  "update_domain",
						Usage: "Change the domain name of a known peer",
						Flags: peerSelectorFlags(
							&cli.StringFlag{
								Name:     "domain",
								Usage:    "peer domain name",
								Required: true,
							},
						),
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiAndPeerIdRequired(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return changePeerDomain(a.api, cmd.String(flagPeerID), cmd.String("domain"), cmd.Writer)
						},
					},
					{
						Name:  "update_ip",
						Usage: "Change the IP address of a known peer",
						Flags: peerSelectorFlags(
							&cli.StringFlag{
								Name:     "ip",
								Usage:    "peer IP address",
								Required: true,
							},
						),
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiAndPeerIdRequired(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return changePeerIP(a.api, cmd.String(flagPeerID), cmd.String("ip"), cmd.Writer)
						},
					},
					{
						Name:  "allow_exit_node",
						Usage: "Allow a known peer to use this device as an exit node (VPN gateway or SOCKS5 proxy)",
						Flags: peerSelectorFlags(
							&cli.BoolFlag{
								Name:     "allow",
								Usage:    "true to allow, false to forbid",
								Required: false,
							},
						),
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiAndPeerIdRequired(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return setAllowUsingAsExitNode(a.api, cmd.String(flagPeerID), cmd.Bool("allow"), cmd.Writer)
						},
					},
					{
						Name:  "invite",
						Usage: "Manage invite links: a peer that uses one is added without a manual accept",
						Commands: []*cli.Command{
							{
								Name:  "create",
								Usage: "Create an invite link and print it with a QR code",
								Flags: []cli.Flag{
									&cli.IntFlag{
										Name:     "uses",
										Usage:    "how many peers may join through the link, up to 100",
										Required: false,
										Value:    1,
									},
									&cli.StringFlag{
										Name:     "expires",
										Usage:    "how long the link lives, up to 365d: a duration such as 30m, 24h, 7d, 1d6h, or 'never'",
										Required: false,
										Value:    "24h",
									},
									&cli.StringFlag{
										Name:     "alias",
										Usage:    "optional. name to give the peer that joins, single-use links only, up to 100 characters",
										Required: false,
									},
									&cli.BoolFlag{
										Name:     "allow-exit-node",
										Usage:    "allow the joining peer to use your device as exit node (as VPN Gateway or SOCKS5 proxy)",
										Required: false,
									},
									&cli.StringFlag{
										Name:     "label",
										Usage:    "note for yourself, shown in the list, up to 100 characters",
										Required: false,
									},
									&cli.BoolFlag{
										Name:     "no-qr",
										Usage:    "print the link without a QR code",
										Required: false,
									},
								},
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiConnection(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return createInvite(a.api, createInviteParams{
										Uses:                 cmd.Int("uses"),
										Expires:              cmd.String("expires"),
										Alias:                cmd.String("alias"),
										AllowUsingAsExitNode: cmd.Bool("allow-exit-node"),
										Label:                cmd.String("label"),
										ShowQR:               !cmd.Bool("no-qr"),
									}, cmd.Writer)
								},
							},
							{
								Name:  "list",
								Usage: "Print all invite links, spent ones included",
								Flags: []cli.Flag{
									&cli.BoolFlag{
										Name:     "show-links",
										Usage:    "print the links themselves, secrets and all",
										Required: false,
									},
								},
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiConnection(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return printInvites(a.api, cmd.Bool("show-links"), cmd.Writer)
								},
							},
							{
								Name:  "revoke",
								Usage: "Stop an invite link from accepting new peers; peers already added stay",
								Flags: []cli.Flag{
									&cli.StringFlag{
										Name:     "id",
										Usage:    "invite id, as printed by 'peers invite list'",
										Required: true,
									},
								},
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiConnection(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return revokeInvite(a.api, cmd.String("id"), cmd.Writer)
								},
							},
						},
					},
				},
			},
			{
				Name:  "gateway",
				Usage: "Manage VPN gateway mode: route all traffic through a peer, or serve as one",
				Commands: []*cli.Command{
					{
						Name:  "status",
						Usage: "Print VPN gateway status",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return gatewayStatus(a.api, cmd.Writer)
						},
					},
					{
						Name:  "client",
						Usage: "Manage VPN gateway client mode (route all traffic through a peer)",
						Commands: []*cli.Command{
							{
								Name:  "use",
								Usage: "Route all traffic through the given peer; if another peer is in use, switch to this one atomically",
								Flags: []cli.Flag{
									&cli.StringFlag{
										Name:     flagPeerID,
										Usage:    "VPN gateway peer id",
										Required: false,
									},
									&cli.StringFlag{
										Name:     flagPeerName,
										Usage:    "VPN gateway peer name",
										Required: false,
									},
								},
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiAndPeerIdRequired(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return gatewayClientUse(a.api, cmd.String(flagPeerID), cmd.Writer)
								},
							},
							{
								Name:  "stop",
								Usage: "Stop using a peer as VPN gateway",
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiConnection(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return gatewayClientStop(a.api, cmd.Writer)
								},
							},
						},
					},
					{
						Name:  "server",
						Usage: "Manage VPN gateway server mode (serve as a gateway for permitted peers)",
						Commands: []*cli.Command{
							{
								Name:  "enable",
								Usage: "Serve as a VPN gateway for permitted peers",
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiConnection(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return gatewaySetServerEnabled(a.api, true, cmd.Writer)
								},
							},
							{
								Name:  "disable",
								Usage: "Stop serving as a VPN gateway",
								Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
									return ctx, a.initApiConnection(cmd)
								},
								Action: func(ctx context.Context, cmd *cli.Command) error {
									return gatewaySetServerEnabled(a.api, false, cmd.Writer)
								},
							},
						},
					},
					{
						Name:  "list",
						Usage: "List peers available as VPN gateways",
						Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
							return ctx, a.initApiConnection(cmd)
						},
						Action: func(ctx context.Context, cmd *cli.Command) error {
							return gatewayList(a.api, cmd.Writer)
						},
					},
				},
			},
			{
				Name:    "logs",
				Aliases: []string{"log"},
				Usage:   "Print application logs, the last 10 lines by default",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:     "head",
						Usage:    "print logs from the beginning of logs",
						Required: false,
					},
					&cli.IntFlag{
						Name:     "n",
						Usage:    "define number of rows of logs to output. Use 0 to print all",
						Required: false,
						Value:    10,
					},
				},
				Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
					return ctx, a.initApiConnection(cmd)
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					logs, err := a.api.ApplicationLog(cmd.Int("n"), cmd.Bool("head"))
					if err != nil {
						return err
					}
					fmt.Fprintln(cmd.Writer, logs)

					return nil
				},
			},
			{
				Name:  "p2p_info",
				Usage: "Print p2p debug info as JSON",
				Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
					return ctx, a.initApiConnection(cmd)
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					debugInfo, err := a.api.P2pDebugInfo()
					if err != nil {
						return err
					}

					bytes, err := json.MarshalIndent(debugInfo, "", "  ")
					if err != nil {
						return err
					}
					fmt.Fprintln(cmd.Writer, string(bytes))

					return nil
				},
			},
			{
				Name:  "update",
				Usage: "Update awl to the latest version",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:     "quiet",
						Aliases:  []string{"q"},
						Usage:    "update without confirmation message",
						Required: false,
					},
					&cli.BoolFlag{
						Name:     "run",
						Aliases:  []string{"r"},
						Usage:    "run after a successful update",
						Required: false,
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					_, _ = fmt.Fprintf(a.cliapp.Writer, "current version: %s\n", config.Version)

					conf, err := config.LoadConfigReadOnly(a.appType)
					if err != nil {
						return fmt.Errorf("update: read config: %v", err)
					}

					updService, err := update.NewUpdateService(conf, a.logger, a.appType)
					if err != nil {
						return fmt.Errorf("update: create update service: %v", err)
					}
					status, err := updService.CheckForUpdates()
					if err != nil {
						return fmt.Errorf("update: check for updates: %v", err)
					}

					if !status {
						a.logger.Infof("app is already up-to-date")
						return nil
					}
					if !cmd.Bool("quiet") {
						status, err = a.yesNoPrompt(fmt.Sprintf("update to version %s: %s, %s", updService.NewVersion.VersionTag(),
							updService.NewVersion.VersionName(), updService.NewVersion.VersionDescription()), true)
						if !status || err != nil {
							a.logger.Info("update stopped")
							return err
						}
					}
					a.logger.Infof("trying to update to version %s: %s", updService.NewVersion.VersionTag(),
						updService.NewVersion.VersionName())
					updResult, err := updService.DoUpdate()
					if err != nil {
						return fmt.Errorf("update: updating process: %v", err)
					}
					a.logger.Infof("updated successfully %s -> %s", conf.Version, updService.NewVersion.VersionTag())
					if cmd.Bool("run") {
						return updResult.DeletePreviousVersionFiles(updaterini.DeleteModRerunExec)
					}
					return updResult.DeletePreviousVersionFiles(updaterini.DeleteModKillProcess)
				},
			},
		},
	}
}

func (a *Application) initApiConnection(cmd *cli.Command) error {
	username := cmd.String("api_user")
	password := cmd.String("api_password")

	apiAddr := cmd.String("api_addr")
	if apiAddr != "" {
		return a.initApiFromAddr(apiAddr, username, password)
	}

	conf, errConfig := config.LoadConfigReadOnly(a.appType)
	if errConfig == nil {
		if username == "" && password == "" {
			username = conf.HttpBasicAuth.Username
			password = conf.HttpBasicAuth.Password
		}
		return a.initApiFromAddr(conf.HttpListenAddress, username, password)
	}

	errDefault := a.initApiFromAddr(defaultApiAddr, username, password)
	if errDefault == nil {
		return nil
	}

	a.logger.Errorf("could not load config file, error: %v", errConfig)
	a.logger.Errorf("could not connect to default api_addr (%s), error: %v", defaultApiAddr, errDefault)

	return errors.New("no connection to api server")
}

func (a *Application) initApiFromAddr(addr, username, password string) error {
	api := apiclient.NewWithAuth(addr, username, password)
	_, err := api.PeerInfo()
	if err != nil {
		return fmt.Errorf("could not access api on address %s: %v", addr, err)
	}

	a.api = api
	return nil
}

// peerSelectorFlags returns flags to select a known peer either by id or by name,
// followed by extra flags. Use with initApiAndPeerId to resolve the name into flagPeerID.
func peerSelectorFlags(extra ...cli.Flag) []cli.Flag {
	flags := make([]cli.Flag, 0, 2+len(extra))
	flags = append(flags,
		&cli.StringFlag{
			Name:     flagPeerID,
			Usage:    "peer id",
			Required: false,
		},
		&cli.StringFlag{
			Name:     flagPeerName,
			Usage:    "peer name",
			Required: false,
		},
	)
	return append(flags, extra...)
}

func (a *Application) initApiAndPeerIdRequired(cmd *cli.Command) error {
	return a.initApiAndPeerId(cmd, true)
}

func (a *Application) initApiAndPeerId(cmd *cli.Command, isRequired bool) error {
	err := a.initApiConnection(cmd)
	if err != nil {
		return err
	}

	pid := cmd.String(flagPeerID)
	if pid != "" {
		return nil
	}
	alias := cmd.String(flagPeerName)
	if alias == "" && isRequired {
		return fmt.Errorf("peerID or name should be defined")
	} else if alias == "" && !isRequired {
		return nil
	}

	pid, err = getPeerIdByAlias(a.api, alias)
	if err != nil {
		return err
	}
	return cmd.Set(flagPeerID, pid)
}

func (a *Application) yesNoPrompt(message string, def bool) (bool, error) {
	choices := "Yes/no, default yes"
	if !def {
		choices = "yes/No, default no"
	}

	r := bufio.NewReader(a.cliapp.Reader)
	var s string

	for {
		_, err := fmt.Fprintf(a.cliapp.Writer, "%s (%s) ", message, choices)
		if err != nil {
			return false, err
		}
		s, _ = r.ReadString('\n')
		s = strings.TrimSpace(s)
		if s == "" {
			return def, nil
		}
		s = strings.ToLower(s)
		if s == "y" || s == "yes" {
			return true, nil
		}
		if s == "n" || s == "no" {
			return false, nil
		}
	}
}
