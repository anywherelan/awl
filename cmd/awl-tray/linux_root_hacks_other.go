//go:build !linux
// +build !linux

package main

import (
	"github.com/skratchdot/open-golang/open"
)

func openURL(input string) error {
	return open.Run(input)
}
