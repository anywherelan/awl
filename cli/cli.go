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
	"github.com/olekukonko/tablewriter"
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
				Name:  "add_peer",
				Usage: "Invite peer or accept existing invitation from this peer",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "peer_id",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "alias",
						Required: false,
					},
				},
				Before: a.initApiConnection,
				Action: func(c *cli.Context) error {
					peerID := c.String("peer_id")
					alias := c.String("alias")

					authRequests, err := a.api.AuthRequests()
					if err != nil {
						return err
					}
					hasRequest := false
					for _, req := range authRequests {
						if req.PeerID == peerID {
							hasRequest = true
							break
						}
					}
					if hasRequest {
						err := a.api.ReplyFriendRequest(peerID, alias, false)
						return err
					}

					err = a.api.SendFriendRequest(peerID, alias)
					return err
				},
			},
			{
				Name:   "auth_requests",
				Usage:  "Print all incoming friend requests",
				Before: a.initApiConnection,
				Action: func(*cli.Context) error {
					authRequests, err := a.api.AuthRequests()
					if err != nil {
						return err
					}
					if len(authRequests) == 0 {
						fmt.Println("has no requests")
						return nil
					}
					for _, req := range authRequests {
						fmt.Printf("Name: '%s' peerID: %s\n", req.Name, req.PeerID)
					}

					return nil
				},
			},
			{
				Name:  "log",
				Usage: "Print logs (default print 10 logs from the end of logs)",
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
				Name:  "peers_status",
				Usage: "Print peers status",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "format",
						Aliases:  []string{"f"},
						Required: false,
						Value:    "npsdaltrcv",
						Usage: "control table columns list and order.Each char add column, write column chars together without gap. Use these chars to add specific columns:\n   " +
							"n - peers number\n   p - peers name\n   s - peers status\n   d - peers domain\n   a - peers ip address\n   l - peers last seen datetime\n   v - peers awl version" +
							"\n   t - total network usage by peer (in/out)\n   r - network usage speed by peer (in/out)\n   c - list of peers connections (IP address + protocol)\n  ",
					},
				},
				Before: a.initApiConnection,
				Action: func(c *cli.Context) error {
					const (
						TableFormatRowNumber  = "n"
						TableFormatPeer       = "p"
						TableFormatStatus     = "s"
						TableFormatDomain     = "d"
						TableFormatAddress    = "a"
						TableFormatLastSeen   = "l"
						TableFormatTotal      = "t"
						TableFormatRate       = "r"
						TableFormatConnection = "c"
						TableFormatVersion    = "v"
					)

					fHeaderMap := map[string]string{
						TableFormatRowNumber:  "№",
						TableFormatPeer:       "peer",
						TableFormatStatus:     "status",
						TableFormatDomain:     "domain",
						TableFormatAddress:    "address",
						TableFormatLastSeen:   "last seen",
						TableFormatTotal:      "total\nin/out, B",
						TableFormatRate:       "rate\nin/out, B",
						TableFormatConnection: "connections\naddress | protocol",
						TableFormatVersion:    "version",
					}

					format := c.String("format")
					if len(format) < 1 {
						return fmt.Errorf("format flag is incorrect: format should contain at leest 1 char")
					}

					headers := make([]string, 0, len(format))
					columns := make([]string, 0, len(format))
					for _, fc := range format {
						fcs := string(fc)
						if _, ok := fHeaderMap[fcs]; !ok {
							return fmt.Errorf("format flag is incorrect: unknown format flat char \"%s\"", fcs)
						}
						headers = append(headers, fHeaderMap[fcs])
						columns = append(columns, fcs)
					}

					peers, err := a.api.KnownPeers()
					if err != nil {
						return err
					}

					table := tablewriter.NewWriter(os.Stdout)
					table.SetBorders(tablewriter.Border{Left: false, Top: false, Right: false, Bottom: false})
					table.SetHeader(headers)
					for i, peer := range peers {
						row := make([]string, 0, len(columns))
						for _, col := range columns {
							switch col {
							case TableFormatRowNumber:
								row = append(row, strconv.Itoa(i+1))
							case TableFormatPeer:
								row = append(row, peer.Name)
							case TableFormatStatus:
								status := "disconnected"
								if peer.Connected {
									status = "connected"
								}
								if !peer.Confirmed {
									status += ", not confirmed"
								}
								row = append(row, status)
							case TableFormatDomain:
								row = append(row, peer.DomainName)
							case TableFormatAddress:
								row = append(row, peer.IpAddr)
							case TableFormatLastSeen:
								row = append(row, peer.LastSeen.Format("2006-01-02 15:04:05"))
							case TableFormatTotal:
								row = append(row,
									fmt.Sprintf("%d/%d", peer.NetworkStats.TotalIn, peer.NetworkStats.TotalOut))
							case TableFormatRate:
								row = append(row,
									fmt.Sprintf("%.2f/%.2f", peer.NetworkStats.RateIn, peer.NetworkStats.RateOut))
							case TableFormatConnection:
								consStr := make([]string, 0, len(peer.Connections))
								for ci, con := range peer.Connections {
									if con.ThroughRelay {
										consStr = append(consStr, fmt.Sprintf("%d) through relay", ci+1))
										continue
									}
									consStr = append(consStr, fmt.Sprintf("%d) %s | %s", ci+1, con.Address, con.Protocol))
								}
								row = append(row, strings.Join(consStr, "\n"))
							case TableFormatVersion:
								row = append(row, peer.Version)
							}
						}
						table.Append(row)
					}
					table.Render()
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
