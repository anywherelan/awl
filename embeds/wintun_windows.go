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
		//nolint:gosec // G306: a DLL next to the binary, not a secret
		err = os.WriteFile(wintunPath, wintunDLL, 0664)
		if err != nil {
			fmt.Printf("error: write wintun.dll file: %v\n", err)
		}
	}
}
