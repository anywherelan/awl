//go:build windows
// +build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed wintun.dll
var wintunDLL []byte

func init() {
	ex, err := os.Executable()
	if err != nil {
		fmt.Printf("error: find executable path: %v\n", err)
		return
	}
	wtPath := filepath.Join(filepath.Dir(ex), "wintun.dll")
	wtStat, wintunFileStatErr := os.Stat(wtPath)
	if !os.IsNotExist(wintunFileStatErr) {
		exStat, err := os.Stat(ex)
		if err != nil {
			fmt.Printf("error: can't get executable file stat: %v\n", err)
			return
		}
		if wtStat.ModTime().After(exStat.ModTime()) {
			return
		}

	}
	err = os.WriteFile(wtPath, wintunDLL, 664)
	if err != nil {
		fmt.Printf("error: write wintun.dll file: %v\n", err)
		return
	}
}
