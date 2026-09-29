//go:build darwin

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ncruces/zenity"
	"github.com/skratchdot/open-golang/open"

	"github.com/anywherelan/awl/config"
)

func initOSSpecificHacks() {
	exitIfTranslocated()

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

// exitIfTranslocated asks the user to move the app to Applications and exits, if the app runs translocated.
// macOS runs a quarantined app that wasn't moved in Finder (e.g. started right from the disk image window)
// from a random read-only path (App Translocation), which changes on every launch.
func exitIfTranslocated() {
	executable, err := os.Executable()
	if err != nil || !strings.Contains(executable, "/AppTranslocation/") {
		return
	}

	fmt.Printf("app is translocated, refusing to run: %s\n", executable)
	err = zenity.Info("Anywherelan can't run from here. Drag it to the Applications folder in Finder, then open it from there.",
		zenity.Title("Move Anywherelan to Applications"))
	if err != nil {
		fmt.Printf("error showing dialog: %v\n", err)
	}
	os.Exit(0)
}

// osascript error number when the user presses Cancel in the password dialog
const osascriptUserCanceled = "(-128)"

func runItselfWithRoot() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Printf("error finding executable path: %v\n", err)
		executable = os.Args[0]
	}

	var stderr bytes.Buffer
	//nolint:gosec
	cmd := exec.Command("osascript", elevateArgs(os.Getuid(), executable)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

	err = cmd.Start()
	if err != nil {
		fmt.Printf("error executing osascript: %v\n", err)
		showElevationErrorDialog(err.Error())
		os.Exit(1)
	}

	err = cmd.Wait()
	if err != nil {
		fmt.Printf("error from waiting osascript to finish: %v\n", err)
		exitCode := cmd.ProcessState.ExitCode()
		if !strings.Contains(stderr.String(), osascriptUserCanceled) {
			showElevationErrorDialog(stderr.String())
		}
		os.Exit(exitCode)
	}

	os.Exit(0)
}

// elevateArgs returns osascript arguments that relaunch executable as root in the GUI session of the user uid.
// Since macOS 10.15 `with administrator privileges` runs the command via launchd (authtrampoline),
// outside the user's GUI session, and on newer macOS the tray icon doesn't show up there.
// `launchctl asuser` moves the process back into the user's session, uid stays 0.
// uid and executable are passed via argv, so the path is never interpolated into the script.
func elevateArgs(uid int, executable string) []string {
	return []string{
		"-e", "on run argv",
		"-e", `do shell script "/bin/launchctl asuser " & (item 1 of argv) & " " & quoted form of (item 2 of argv) ` +
			`with prompt "Anywherelan needs administrator rights to create the virtual network interface." ` +
			`with administrator privileges`,
		"-e", "end run",
		strconv.Itoa(uid), executable,
	}
}

// showElevationErrorDialog is used before the app is initialized, so it doesn't use logger like showErrorDialog
func showElevationErrorDialog(details string) {
	const maxLen = 1000
	details = strings.TrimSpace(details)
	if len(details) > maxLen {
		details = "…" + details[len(details)-maxLen:]
	}
	message := "Anywherelan could not start with administrator rights."
	if details != "" {
		message += "\n\n" + details
	}
	err := zenity.Error(message, zenity.Title("Anywherelan error"), zenity.ErrorIcon)
	if err != nil {
		fmt.Printf("error showing dialog: %v\n", err)
	}
}
