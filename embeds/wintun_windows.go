//go:build windows
// +build windows

package embeds

import (
	_ "embed"
	"fmt"
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

	equal := checkIsFileEqual(wintunPath, wintunDLL)
	if !equal {
		err = os.WriteFile(wintunPath, wintunDLL, 664)
		if err != nil {
			fmt.Printf("error: write wintun.dll file: %v\n", err)
		}
	}
}
