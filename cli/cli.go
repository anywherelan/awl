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
	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/update"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-eventbus"
	"github.com/urfave/cli/v2"
)

var defaultApiAddr = "127.0.0.1:" + strconv.Itoa(config.DefaultHTTPPort)

type Application struct {
	logger *log.ZapEventLogger
	api    *apiclient.Client
	cliapp *cli.App
}

func New() *Application {
	app := new(Application)
	app.logger = log.Logger("awl/cli")
	app.init()

	return app
}

func (a *Application) Run() {
	if len(os.Args) == 1 {
		return
	} else if os.Args[1] != "cli" {
		a.logger.Fatalf("Unknown command '%s', try 'awl cli -h' for info on cli commands or 'awl' to start awl server", os.Args[1])
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
		HelpName: path.Base(os.Args[0]) + " cli",
		Version:  config.Version,
		Usage:    "p2p mesh vpn",
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
				Usage: "group of functions to work with your stats and settings",
				Subcommands: []*cli.Command{
					{
						Name:   "stats",
						Usage:  "print your stats",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printStatus(a.api)
						},
					},
					{
						Name:   "id",
						Usage:  "print your peer id",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printPeerId(a.api)
						},
					},
				},
			},
			{
				Name:  "peers",
				Usage: "group of functions to work with peers. Use for check friend requests and peers status",
				Subcommands: []*cli.Command{
					{
						Name:  "status",
						Usage: "print peers status",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "format",
								Aliases:  []string{"f"},
								Required: false,
								Value:    "npsdaltrcv",
								Usage: "control table columns list and order.Each char add column, write column chars together without gap. Use these chars to add specific columns:\n   " +
									"n - peers number\n   p - peers name\n   i - peers id\n   s - peers status\n   d - peers domain\n   a - peers ip address\n   l - peers last seen datetime\n   v - peers awl version" +
									"\n   t - total network usage by peer (in/out)\n   r - network usage speed by peer (in/out)\n   c - list of peers connections (IP address + protocol)\n  ",
							},
						},
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printPeersStatus(a.api, c.String("format"))
						},
					},
					{
						Name:   "requests",
						Usage:  "print all incoming friend requests",
						Before: a.initApiConnection,
						Action: func(c *cli.Context) error {
							return printFriendRequests(a.api)
						},
					},
				},
			},
			{
				Name:  "peer",
				Usage: "group of commands to work with peer (one of the flags is required)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "pid",
						Usage:    "peer id",
						Required: false,
					},
					&cli.StringFlag{
						Name:     "alias",
						Required: false,
					},
				},
				Before: func(c *cli.Context) error {
					err := a.initApiConnection(c)
					if err != nil {
						return err
					}

					pid := c.String("pid")
					if pid != "" {
						return nil
					}
					alias := c.String("alias")
					if alias == "" {
						return fmt.Errorf("peerID or alias should be defined")
					}

					pid, err = getPeerIdByAlias(a.api, alias)
					if err != nil {
						return err
					}
					return c.Set("pid", pid)
				},
				Subcommands: []*cli.Command{
					{
						Name:  "add",
						Usage: "invite peer or accept existing invitation from this peer",
						Action: func(c *cli.Context) error {
							return addPeer(a.api, c.String("pid"), c.String("alias"))
						},
					},
					{
						Name:  "remove",
						Usage: "remove peer from friend list",
						Action: func(c *cli.Context) error {
							return removePeer(a.api, c.String("pid"))
						},
					},
					{
						Name:  "ch_alias",
						Usage: "change known peer alias",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:     "alias",
								Required: true,
							},
						},
						Action: func(c *cli.Context) error {
							return changePeerAlias(a.api, c.String("pid"), c.String("alias"))
						},
					},
				},
			},
			{
				Name:  "log",
				Usage: "print logs (default print 10 logs from the end of logs)",
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
				Usage:  "Print p2p debug info",
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
				Usage: "update awl to the latest version",
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
					conf, err := config.LoadConfig(eventbus.NewBus())
					if err != nil {
						return fmt.Errorf("update: read config: %v", err)
					}
					updService, err := update.NewUpdateService(conf, a.logger, update.AppTypeAwl)
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
