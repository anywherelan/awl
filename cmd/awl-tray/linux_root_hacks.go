//go:build linux
// +build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

// TODO: update godbus/dbus and remove module replace when https://github.com/godbus/dbus/pull/247 will be merged
func init() {
	sudoUidStr := os.Getenv("SUDO_UID")
	sudoUid, sudoUiderr := strconv.Atoi(sudoUidStr)
	if sudoUiderr == nil {
		connectSessionDbus(sudoUid)
		return
	}

	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	userUid, err := strconv.Atoi(strings.TrimPrefix(xdgRuntimeDir, "/run/user/"))
	if err != nil {
		fmt.Printf("neither SUDO_UID ('%s') nor XDG_RUNTIME_DIR ('%s') were set\n", sudoUidStr, xdgRuntimeDir)
		return
	}

	connectSessionDbus(userUid)
}

// to access another user's dbus session under root we need DBUS_SESSION_BUS_ADDRESS env var to be set and connect under user's uid
// see https://stackoverflow.com/questions/6496847/access-another-users-d-bus-session
func connectSessionDbus(userUid int) {
	err := syscall.Seteuid(userUid)
	if err != nil {
		fmt.Printf("syscall.Seteuid(%d): %v\n", userUid, err)
		return
	}
	_, err = dbus.SessionBus()
	if err != nil {
		fmt.Println("connect session dbus:", err)
	}

	err = syscall.Seteuid(0)
	if err != nil {
		fmt.Println("syscall.Seteuid(0):", err)
	}
}
