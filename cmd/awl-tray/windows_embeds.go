//go:build windows
// +build windows

package main

import (
	"github.com/anywherelan/awl/embeds"
)

func init() {
	embeds.EmbedWintun()
}
