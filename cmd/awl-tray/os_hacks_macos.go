//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/anywherelan/awl/config"
	"github.com/skratchdot/open-golang/open"
)

func initOSSpecificHacks() {
	uid := os.Geteuid()
	if uid != 0 {
		fmt.Printf("process is run under non-root uid: %d, ask for root permissions with osascript\n", uid)
		runItselfWithRoot()
		return
	}

	// this is required to allow listening on config.AdminHttpServerIP address
	//nolint:gosec
	err := exec.Command("ifconfig", "lo0", "alias", config.AdminHttpServerIP, "up").Run()
	if err != nil {
		fmt.Printf("error: `ifconfig lo0 alias %s up`: %v\n", config.AdminHttpServerIP, err)
	}
}

func openURL(input string) error {
	return open.Run(input)
}

func getRealUserID() (uint32, bool) {
	return 0, false
}

func runItselfWithRoot() {
	// TODO: show pop-up describing that awl needs root for vpn? show it only on first launch?

	executable, err := os.Executable()
	if err != nil {
		fmt.Printf("error finding executable path: %v\n", err)
		executable = os.Args[0]
	}

	osaScript := fmt.Sprintf("do shell script \"%s\" with administrator privileges", executable)

	//nolint:gosec
	cmd := exec.Command("osascript", "-e", osaScript)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		fmt.Printf("error executing osascript: %v\n", err)
		os.Exit(1)
	}

	err = cmd.Wait()
	if err != nil {
		fmt.Printf("error from waiting osascript to finish: %v\n", err)
		exitCode := cmd.ProcessState.ExitCode()
		os.Exit(exitCode)
	}

	os.Exit(0)
}
