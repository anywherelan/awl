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
)

const logRootHacks = false

func logRootHack(format string, a ...any) {
	if logRootHacks {
		fmt.Printf(format+"\n", a...)
	}
}

var nonRootUid int

func initOSSpecificHacks() {
	uid := os.Geteuid()
	if uid != 0 {
		logRootHack("process is run under non-root uid: %d", uid)
		return
	}

	uid, err := getUserIdUnderRoot()
	if err != nil {
		fmt.Printf("error getting user id under root: %v\n", err)
		return
	}
	nonRootUid = uid
	logRootHack("found uid under root: %d", uid)

	logRootHack("start connectSessionDbus")
	connectSessionDbus(nonRootUid)
	logRootHack("end connectSessionDbus")

	display := os.Getenv("DISPLAY")
	logRootHack("got DISPLAY from env: '%s'", display)
	if display == "" {
		defaultDisplay := ":0"
		logRootHack("don't have DISPLAY from env, set to '%s'", defaultDisplay)
		err = os.Setenv("DISPLAY", defaultDisplay)
		if err != nil {
			fmt.Printf("error setenv DISPLAY: %v\n", err)
		}
	}
}

// to access another user's dbus session under root we need DBUS_SESSION_BUS_ADDRESS env var to be set and connect under user's uid
// see https://stackoverflow.com/questions/6496847/access-another-users-d-bus-session
func connectSessionDbus(userUid int) {
	logRootHack("call syscall.Seteuid(%d)", userUid)
	err := syscall.Seteuid(userUid)
	if err != nil {
		fmt.Printf("error from calling syscall.Seteuid(%d): %v\n", userUid, err)
		return
	}

	dbusAddress := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	logRootHack("got DBUS_SESSION_BUS_ADDRESS from env: '%s'", dbusAddress)

	if dbusAddress == "" {
		defaultDbusAddress := fmt.Sprintf("unix:path=/run/user/%d/bus", userUid)
		logRootHack("don't have DBUS_SESSION_BUS_ADDRESS from env, set to '%s'", defaultDbusAddress)
		err = os.Setenv("DBUS_SESSION_BUS_ADDRESS", defaultDbusAddress)
		if err != nil {
			fmt.Printf("error setenv DBUS_SESSION_BUS_ADDRESS: %v\n", err)
		}
	}

	_, err = dbus.SessionBus()
	if err != nil {
		fmt.Printf("error connecting session dbus: %v\n", err)
	}

	logRootHack("call syscall.Seteuid(0)")
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
