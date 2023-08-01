//go:build !linux && !darwin
// +build !linux,!darwin

package main

import (
	"github.com/skratchdot/open-golang/open"

	"github.com/anywherelan/awl/embeds"
)

func initOSSpecificHacks() {
	embeds.EmbedIcon()
}

func openURL(input string) error {
	return open.Run(input)
}

func getRealUserID() (uint32, bool) {
	return 0, false
}
