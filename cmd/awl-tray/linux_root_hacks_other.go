//go:build !linux
// +build !linux

package main

import (
	"github.com/skratchdot/open-golang/open"
)

func initOSSpecificHacks() {
}

func openURL(input string) error {
	return open.Run(input)
}

func getRealUserID() (uint32, bool) {
	return 0, false
}
