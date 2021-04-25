package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/application/pkg"
	"github.com/anywherelan/awl/config"
	"github.com/ipfs/go-log/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/urfave/cli/v2"
)

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
		a.logger.Fatal("Unknown command '%s'", os.Args[1])
	}
	err := a.cliapp.Run(os.Args[1:])
	if err != nil {
		a.logger.Fatalf("Error occurred: %v", err)
	}

	os.Exit(0)
}

func (a *Application) init() {
	var apiAddr string
	a.cliapp = &cli.App{
		Name:    "awl",
		Version: pkg.Version,
		Usage:   "p2p mesh vpn",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "api_addr",
				Usage:       "awl api address, example: 127.0.0.1:8639",
				Required:    false,
				Destination: &apiAddr,
			},
		},
		Before: func(_ *cli.Context) (err error) {
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
			conf, err := config.LoadConfig()
			if err != nil {
				return fmt.Errorf("could not load config, api_addr does not set, exit (%v)", err)
			}
			addr = conf.HttpListenAddress
			if addr == "" {
				return errors.New("httpListenAddress from config is empty")
			}

			return nil
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
						err := a.api.AcceptFriendRequest(peerID, alias)
						return err
					}

					err = a.api.SendFriendRequest(peerID, alias)
					return err
				},
			},
			{
				Name:  "auth_requests",
				Usage: "Print all incoming friend requests",
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
				Name:  "p2p_info",
				Usage: "Print p2p debug info",
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
					&cli.BoolFlag{
						Name:     "short",
						Aliases:  []string{"s"},
						Required: false,
					},
				},
				Action: func(c *cli.Context) error {
					peers, err := a.api.KnownPeers()
					if err != nil {
						return err
					}

					if c.Bool("short") {
						table := tablewriter.NewWriter(os.Stdout)
						table.SetBorders(tablewriter.Border{Left: false, Top: false, Right: false, Bottom: false})
						table.SetHeader([]string{"peer", "status", "address"})
						for _, peer := range peers {
							status := "disconnected"
							if peer.Connected {
								status = "connected"
							}
							if !peer.Confirmed {
								status += ", not confirmed"
							}
							table.Append([]string{peer.Name, status, peer.IpAddr})
						}
						table.Render()
						return nil
					}

					return errors.New("print full info is not implemented, use flag --short instead")
				},
			},
		},
	}
}
