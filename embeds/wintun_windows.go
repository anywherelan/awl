//go:build windows
// +build windows

package embeds

import (
	"bufio"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

//go:embed wintun.dll
var wintunDLL []byte

func EmbedWintun() {
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
	existedWintunClosed := false
	closeExistedWintun := func() {
		if existedWintunClosed {
			return
		}
		err := existedWintun.Close()
		if err != nil {
			fmt.Printf("error: close read wintun.dll file: %v\n", err)
		}
		existedWintunClosed = true
	}
	defer closeExistedWintun()
	equal, err := streamsEqual(bytes.NewReader(wintunDLL), bufio.NewReader(existedWintun))
	if err != nil {
		fmt.Printf("error: compare wintun.dll files: %v\n", err)
	}
	if !equal {
		closeExistedWintun()
		writeWintun()
	}
}

func streamsEqual(s1 io.Reader, s2 io.Reader) (bool, error) {
	const chunkSize = 4096
	buf1 := make([]byte, chunkSize)
	buf2 := make([]byte, chunkSize)
	for {
		len1, err1 := io.ReadFull(s1, buf1)
		len2, err2 := io.ReadFull(s2, buf2)
		if (err1 != nil && err1 != io.ErrUnexpectedEOF) || (err2 != nil && err2 != io.ErrUnexpectedEOF) {
			if (err1 == io.EOF || err1 == nil) && (err2 == io.EOF || err2 == nil) {
				return err1 == io.EOF && err2 == io.EOF, nil
			}
			return false, fmt.Errorf("compare streams reading err: source1 err: %v; source2 err: %v", err1, err2)
		}
		if !bytes.Equal(buf1[:len1], buf2[:len2]) {
			return false, nil
		}
	}
}
