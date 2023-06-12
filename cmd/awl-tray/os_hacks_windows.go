//go:build windows
// +build windows

package main

import (
	"fmt"

	"github.com/skratchdot/open-golang/open"

	"github.com/anywherelan/awl/embeds"
)

func initOSSpecificHacks() {
	_, err := embeds.EmbedIcon()
	if err != nil {
		fmt.Printf("error: create icon: %v", err)
	}
}

func openURL(input string) error {
	return open.Run(input)
}

func getRealUserID() (uint32, bool) {
	return 0, false
}

func removeIcon() error {
	return nil
}
