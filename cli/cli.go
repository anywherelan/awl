package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/GrigoryKrasnochub/updaterini"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/urfave/cli/v2"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/update"
)

const (
	WithEnvCommandName = "with-env"
	CliCommandName     = "cli"
)

var defaultApiAddr = "127.0.0.1:" + strconv.Itoa(config.DefaultHTTPPort)

var binaryName = path.Base(os.Args[0])

type Application struct {
	logger     *log.ZapEventLogger
	api        *apiclient.Client
	cliapp     *cli.App
	updateType update.ApplicationType
}

func New(updateType update.ApplicationType) *Application {
	app := new(Application)
	app.logger = log.Logger("awl/cli")
	app.updateType = updateType
	app.init()

	return app
}

func (a *Application) Run() {
	if len(os.Args) == 1 {
		return
	} else if os.Args[1] == WithEnvCommandName {
		// is handled in linux_root_hacks.go
		return
	} else if os.Args[1] == CliCommandName {
		// ok, handle here below
	} else {
		a.logger.Fatalf("Unknown command '%s', try '%s cli -h' for info on cli commands or '%s' to start awl server", os.Args[1], binaryName, binaryName)
	}

	err := a.cliapp.Run(os.Args[1:])
	if err != nil {
		a.logger.Fatalf("Error occurred: %v", err)
	}

	os.Exit(0)
}

func (a *Application) init() {
	a.cliapp = &cli.App{
		Name:     "awl",
		HelpName: binaryName + " " + CliCommandName,
		Description: "Anywherelan (awl for brevity) is a mesh VPN project, similar to tinc, direct wireguard or tailscale. " +
			"Awl makes it easy to connect to any of your devices (at the IP protocol level) wherever they are." +
			"\nSee more at the project page https://github.com/anywherelan/awl",
		Version: config.Version,
		Usage:   "p2p mesh vpn",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "api_addr",
				Usage:    fmt.Sprintf("awl api address, example: %s", defaultApiAddr),
				Required: false,
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "me",
				Usage: "Group of commands to work with your status and settings",
				Subcommands: []*cli.Command{
					{
						Name:   "status",
						Usage:  "Print your server status, network stats",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printStatus(a.api)
						},
					},
					{
						Name:   "id",
						Usage:  "Print your peer id",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printPeerId(a.api)
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
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return renameMe(a.api, c.String("name"))
						},
					},
					{
						Name:   "list_proxies",
						Usage:  "Prints list of available SOCKS5 proxies",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return listProxies(a.api)
						},
					},
					{
						Name:  "set_proxy",
						Usage: "Sets SOCKS5 proxy for your peer, empty pid/name means disable proxy",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "pid",
								Usage:    "peer id",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: false,
							},
						},
						Before: func(c *cli.Context) error {
							return a.initApiAndPeerId(c, false)
						},
						Action: func(c *cli.Context) error {
							return setProxy(a.api, c.String("pid"))
						},
					},
				},
			},
			{
				Name:  "peers",
				Usage: "Group of commands to work with peers. Use to check friend requests and work with known peers",
				Subcommands: []*cli.Command{
					{
						Name:  "status",
						Usage: "Print peers status",
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
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printPeersStatus(a.api, c.String("format"))
						},
					},
					{
						Name:   "requests",
						Usage:  "Print all incoming friend requests",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printFriendRequests(a.api)
						},
					},
					{
						Name:  "add",
						Usage: "Invite peer or accept existing invitation from this peer",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "pid",
								Usage:    "peer id",
								Required: true,
							},
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: true,
							},
						},
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return addPeer(a.api, c.String("pid"), c.String("name"))
						},
					},
					{
						Name:  "remove",
						Usage: "Remove peer from the friends list",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "pid",
								Usage:    "peer id",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: false,
							},
						},
						Before: a.initApiAndPeerIdRequired,
						Action: func(c *cli.Context) error {
							return removePeer(a.api, c.String("pid"))
						},
					},
					{
						Name:  "rename",
						Usage: "Change known peer name",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "pid",
								Usage:    "peer id",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "new_name",
								Usage:    "peer new name",
								Required: true,
							},
						},
						Before: a.initApiAndPeerIdRequired,
						Action: func(c *cli.Context) error {
							return changePeerAlias(a.api, c.String("pid"), c.String("new_name"))
						},
					},
					{
						Name:  "update_domain",
						Usage: "Change known peer domain name",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "pid",
								Usage:    "peer id",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "domain",
								Usage:    "peer domain name",
								Required: true,
							},
						},
						Before: a.initApiAndPeerIdRequired,
						Action: func(c *cli.Context) error {
							return changePeerDomain(a.api, c.String("pid"), c.String("domain"))
						},
					},
					{
						Name:  "allow_exit_node",
						Usage: "Allow known peer to use this device as exit node (as socks5 proxy)",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "pid",
								Usage:    "peer id",
								Required: false,
							},
							&cli.StringFlag{
								Name:     "name",
								Usage:    "peer name",
								Required: false,
							},
							&cli.BoolFlag{
								Name:     "allow",
								Usage:    "allow",
								Required: false,
							},
						},
						Before: a.initApiAndPeerIdRequired,
						Action: func(c *cli.Context) error {
							return setAllowUsingAsExitNode(a.api, c.String("pid"), c.Bool("allow"))
						},
					},
				},
			},
			{
				Name:    "logs",
				Aliases: []string{"log"},
				Usage:   "Prints application logs (default print 10 logs from the end of logs)",
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
				Before: a.initApiConnection,
				Action: func(c *cli.Context) error {
					logs, err := a.api.ApplicationLog(c.Int("n"), c.Bool("head"))
					if err != nil {
						return err
					}
					fmt.Println(logs)

					return nil
				},
			},
			{
				Name:   "p2p_info",
				Usage:  "Prints p2p debug info",
				Before: a.initApiConnection,
				Action: func(*cli.Context) error {
					debugInfo, err := a.api.P2pDebugInfo()
					if err != nil {
						return err
					}

					bytes, err := json.MarshalIndent(debugInfo, "", "  ")
					if err != nil {
						return err
					}
					fmt.Println(string(bytes))

					return nil
				},
			},
			{
				Name:  "update",
				Usage: "Updates awl to the latest version",
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
				Action: func(c *cli.Context) error {
					_, _ = fmt.Fprintf(a.cliapp.Writer, "current version: %s\n", config.Version)

					conf, err := config.LoadConfig(eventbus.NewBus())
					if err != nil {
						return fmt.Errorf("update: read config: %v", err)
					}

					updService, err := update.NewUpdateService(conf, a.logger, a.updateType)
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
					if !c.Bool("quiet") {
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
					if c.Bool("run") {
						return updResult.DeletePreviousVersionFiles(updaterini.DeleteModRerunExec)
					}
					return updResult.DeletePreviousVersionFiles(updaterini.DeleteModKillProcess)
				},
			},
		},
	}
}

func (a *Application) initApiConnection(c *cli.Context) (err error) {
	apiAddr := c.String("api_addr")
	var addr string
	defer func() {
		if err != nil {
			return
		}
		a.api = apiclient.New(addr)
		_, err2 := a.api.PeerInfo()
		if err2 != nil {
			err = fmt.Errorf("could not access api on address %s: %v", addr, err2)
		}
	}()
	if apiAddr != "" {
		addr = apiAddr
		return nil
	}
	conf, err := config.LoadConfig(eventbus.NewBus())
	if err != nil {
		a.logger.Errorf("could not load config, use default api_addr (%s), error: %v", defaultApiAddr, err)
		addr = defaultApiAddr
		return nil
	}
	addr = conf.HttpListenAddress
	if addr == "" {
		return errors.New("httpListenAddress from config is empty")
	}

	return nil
}

func (a *Application) initApiAndPeerIdRequired(c *cli.Context) error {
	return a.initApiAndPeerId(c, true)
}

func (a *Application) initApiAndPeerId(c *cli.Context, isRequired bool) error {
	err := a.initApiConnection(c)
	if err != nil {
		return err
	}

	pid := c.String("pid")
	if pid != "" {
		return nil
	}
	alias := c.String("name")
	if alias == "" && isRequired {
		return fmt.Errorf("peerID or name should be defined")
	} else if alias == "" && !isRequired {
		return nil
	}

	pid, err = getPeerIdByAlias(a.api, alias)
	if err != nil {
		return err
	}
	return c.Set("pid", pid)
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
