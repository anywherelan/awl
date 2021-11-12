//go:build windows
// +build windows

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anywherelan/awl/libs"
)

//go:embed wintun.dll
var wintunDLL []byte

func init() {
	ex, err := os.Executable()
	if err != nil {
		fmt.Printf("error: find executable path: %v\n", err)
		return
	}
	wintunPath := filepath.Join(filepath.Dir(ex), "wintun.dll")
	writeWintun := func() {
		err = os.WriteFile(wintunPath, wintunDLL, 664)
		if err != nil {
			fmt.Printf("error: write wintun.dll file: %v\n", err)
		}
	}

	// wintun does not exist

	if _, err := os.Stat(wintunPath); errors.Is(err, os.ErrNotExist) {
		writeWintun()
		return
	}

	// wintun exist

	existedWintun, err := os.Open(wintunPath)
	if err != nil {
		fmt.Printf("error: read wintun.dll file: %v\n", err)
		return
	}
	defer func() {
		err := existedWintun.Close()
		if err != nil {
			fmt.Printf("error: close read wintun.dll file: %v\n", err)
		}
	}()
	equal, err := libs.StreamsEqual(bytes.NewReader(wintunDLL), bufio.NewReader(existedWintun))
	if err != nil {
		fmt.Printf("error: compare wintun.dll files: %v\n", err)
	}
	if !equal {
		writeWintun()
	}
}
