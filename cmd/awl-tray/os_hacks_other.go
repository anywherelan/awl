//go:build !linux
// +build !linux

package main

import (
	"os"

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

func removeIcon() error {
	return os.Remove(embeds.GetIconPath())
}
