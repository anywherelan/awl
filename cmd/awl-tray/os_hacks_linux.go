//go:build linux
// +build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"

	"github.com/anywherelan/awl/cli"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/embeds"
)

var requiredEnvVars = [...]string{
	config.AppDataDirEnvKey,
	"DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS",
	"XDG_CONFIG_HOME", "HOME", "XDG_RUNTIME_DIR", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE",
}

const logHacks = false

func logHack(format string, a ...any) {
	if logHacks {
		fmt.Printf(format+"\n", a...)
	}
}

var nonRootUid int

func initOSSpecificHacks() {
	uid := os.Geteuid()
	if uid != 0 {
		logHack("process is run under non-root uid: %d", uid)
		runItselfWithRoot()
		return
	}

	setEnvFromArgs()

	uid, err := getUserIdUnderRoot()
	if err != nil {
		fmt.Printf("error getting user id under root: %v\n", err)
		return
	}
	nonRootUid = uid
	config.LinuxFilesOwnerUID = uid
	logHack("found uid under root: %d", uid)

	logHack("start connectSessionDbus")
	connectSessionDbus(nonRootUid)
	logHack("end connectSessionDbus")

	display := os.Getenv("DISPLAY")
	logHack("got DISPLAY from env: '%s'", display)
	if display == "" {
		defaultDisplay := ":0"
		logHack("don't have DISPLAY from env, set to '%s'", defaultDisplay)
		err = os.Setenv("DISPLAY", defaultDisplay)
		if err != nil {
			fmt.Printf("error setenv DISPLAY: %v\n", err)
		}
	}

	iconPath, err := embeds.EmbedIcon()
	if err != nil {
		fmt.Printf("error: create icon: %v\n", err)
		return
	}
	err = embeds.EmbedDesktopFile(iconPath)
	if err != nil {
		fmt.Printf("error: create desktop file: %v\n", err)
	}
}

func runItselfWithRoot() {
	// TODO: show pop-up describing that awl needs root for vpn? show it only on first launch?

	executable, err := os.Executable()
	if err != nil {
		fmt.Printf("error finding executable path: %v\n", err)
		executable = os.Args[0]
	}

	args := []string{executable, cli.WithEnvCommandName}

	for _, key := range requiredEnvVars {
		value := os.Getenv(key)
		if value == "" {
			continue
		}
		args = append(args, fmt.Sprintf("%s=%s", key, value))
	}

	cmd := exec.Command("pkexec", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		fmt.Printf("error executing pkexec: %v\n", err)
		os.Exit(1)
	}

	err = cmd.Wait()
	if err != nil {
		fmt.Printf("error from waiting pkexec to finish: %v\n", err)
		exitCode := cmd.ProcessState.ExitCode()
		os.Exit(exitCode)
	}

	os.Exit(0)
}

func setEnvFromArgs() {
	if len(os.Args) < 3 || os.Args[1] != cli.WithEnvCommandName {
		return
	}

	envs := os.Args[2:]
	for _, env := range envs {
		key, val, exists := strings.Cut(env, "=")
		if exists {
			_ = os.Setenv(key, val)
		}
	}
}

// to access another user's dbus session under root we need DBUS_SESSION_BUS_ADDRESS env var to be set and connect under user's uid
// see https://stackoverflow.com/questions/6496847/access-another-users-d-bus-session
func connectSessionDbus(userUid int) {
	logHack("call syscall.Seteuid(%d)", userUid)
	err := syscall.Seteuid(userUid)
	if err != nil {
		fmt.Printf("error from calling syscall.Seteuid(%d): %v\n", userUid, err)
		return
	}

	dbusAddress := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	logHack("got DBUS_SESSION_BUS_ADDRESS from env: '%s'", dbusAddress)

	if dbusAddress == "" {
		defaultDbusAddress := fmt.Sprintf("unix:path=/run/user/%d/bus", userUid)
		logHack("don't have DBUS_SESSION_BUS_ADDRESS from env, set to '%s'", defaultDbusAddress)
		err = os.Setenv("DBUS_SESSION_BUS_ADDRESS", defaultDbusAddress)
		if err != nil {
			fmt.Printf("error setenv DBUS_SESSION_BUS_ADDRESS: %v\n", err)
		}
	}

	_, err = dbus.SessionBus()
	if err != nil {
		fmt.Printf("error connecting session dbus: %v\n", err)
	}

	logHack("call syscall.Seteuid(0)")
	err = syscall.Seteuid(0)
	if err != nil {
		fmt.Printf("error from calling syscall.Seteuid(0): %v\n", err)
	}
}

func getUserIdUnderRoot() (int, error) {
	sudoUidStr := os.Getenv("SUDO_UID")
	sudoUid, sudoUiderr := strconv.Atoi(sudoUidStr)
	if sudoUiderr == nil {
		return sudoUid, nil
	}

	pkexecUidStr := os.Getenv("PKEXEC_UID")
	pkexecUid, pkexecUiderr := strconv.Atoi(pkexecUidStr)
	if pkexecUiderr == nil {
		return pkexecUid, nil
	}

	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	userUid, err := strconv.Atoi(strings.TrimPrefix(xdgRuntimeDir, "/run/user/"))
	if err == nil {
		return userUid, nil
	}

	return 0, fmt.Errorf("neither SUDO_UID ('%s') nor XDG_RUNTIME_DIR ('%s') nor PKEXEC_UID ('%s') were set\n", sudoUidStr, xdgRuntimeDir, pkexecUidStr)
}

func getRealUserID() (uint32, bool) {
	return uint32(nonRootUid), nonRootUid != 0
}

func openURL(input string) error {
	uid, hasUID := getRealUserID()

	cmd := exec.Command("xdg-open", input)
	if hasUID {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Gid: uid, Uid: uid},
		}
	}

	return cmd.Run()
}
