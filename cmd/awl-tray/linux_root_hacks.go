//go:build linux
// +build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

var nonRootUid int

var errUnknownUid = errors.New("unknown non-root uid")

func init() {
	uid, err := getUserIdUnderRoot()
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	nonRootUid = uid

	connectSessionDbus(nonRootUid)
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

func getUserIdUnderRoot() (int, error) {
	sudoUidStr := os.Getenv("SUDO_UID")
	sudoUid, sudoUiderr := strconv.Atoi(sudoUidStr)
	if sudoUiderr == nil {
		return sudoUid, nil
	}

	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	userUid, err := strconv.Atoi(strings.TrimPrefix(xdgRuntimeDir, "/run/user/"))
	if err == nil {
		return userUid, nil
	}

	return 0, fmt.Errorf("neither SUDO_UID ('%s') nor XDG_RUNTIME_DIR ('%s') were set\n", sudoUidStr, xdgRuntimeDir)
}

func openURL(input string) error {
	if nonRootUid == 0 {
		return errUnknownUid
	}

	cmd := exec.Command("xdg-open", input)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Gid: uint32(nonRootUid), Uid: uint32(nonRootUid)},
	}
	return cmd.Run()
}
