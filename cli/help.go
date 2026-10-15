package cli

import (
	"fmt"
	"runtime"

	"github.com/anywherelan/awl/config"
)

const (
	projectTagline = "peer-to-peer mesh VPN, no coordination server"
	websiteURL     = "https://anywherelan.com"
	docsURL        = "https://github.com/anywherelan/awl#table-of-contents"
)

// cliDescription is the DESCRIPTION of `awl cli --help`.
const cliDescription = `Anywherelan (awl) connects your devices into a private network at the IP level,
wherever they are: each one gets a VPN IP and a name.awl domain. Peers find each
other through a DHT and connect directly, falling back to relays behind strict
NATs. No account, no central server.

A device can also be an exit node for others: a full-tunnel VPN gateway
(see 'gateway') or a SOCKS5 proxy (see 'me set_proxy').

This CLI controls a running awl server through its local HTTP API.`

// cliHelpFooter follows the standard app help, after GLOBAL OPTIONS.
const cliHelpFooter = `
EXAMPLES:
   {{.HelpName}} me status                                # server status and network stats
   {{.HelpName}} me id                                    # your peer id and a link to share
   {{.HelpName}} peers status                             # known peers and their status
   {{.HelpName}} peers invite create                      # link that adds a peer without a manual accept
   {{.HelpName}} peers add --pid <peer id> --name laptop  # invite a peer, or accept its invitation

LEARN MORE:
   Website:  ` + websiteURL + `
   CLI docs: https://github.com/anywherelan/awl#terminal-based-client
   Issues:   https://github.com/anywherelan/awl/issues
`

// startsWithoutCommand names what the binary starts when run without a command.
func (a *Application) startsWithoutCommand() string {
	if a.appType == config.AppTypeAwlTray {
		return "the tray app and the server"
	}
	return "the server"
}

// printGlobalHelp prints `awl -h`, the help of the binary itself. The CLI has
// its own, longer help: `awl cli --help`.
func (a *Application) printGlobalHelp() {
	fmt.Printf(`Anywherelan (awl): %s.

Usage: %s [command]

Run without a command to start %s.

Commands:
  cli    Control the running server: peers, invite links, VPN gateway, logs

Flags:
  -h, --help     Show this help.
  -v, --version  Show version.

Run "%s cli --help" for the list of CLI commands.

Website: %s
Docs:    %s
`, projectTagline, binaryName, a.startsWithoutCommand(), binaryName, websiteURL, docsURL)
}

func (a *Application) printVersion() {
	fmt.Printf("Anywherelan version %s (%s %s-%s)\n", config.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
