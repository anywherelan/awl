//go:build linux
// +build linux

package embeds

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anywherelan/awl/config"
)

//go:embed .desktop
var desktopFile []byte

const (
	desktopFileMod  = 0764 // rwxrw-r--
	desktopFileName = "awl.desktop"
	desktopFileDir  = ".local/share/applications"
)

func EmbedDesktopFile(iconPath string) error {
	if config.IsDevVersion() {
		return nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("error: get home dir: %w", err)
	}

	ex, err := os.Executable()
	if err != nil {
		return fmt.Errorf("error: find executable path: %w", err)
	}

	replacements := map[string][]byte{
		"{ICONPATH}": []byte(iconPath),
		"{EXECPATH}": []byte(ex),
	}

	desktopFileCopy := bytes.Clone(desktopFile)
	for replName, replVal := range replacements {
		desktopFileCopy = bytes.ReplaceAll(desktopFileCopy, []byte(replName), replVal)
	}

	filePath := filepath.Join(homeDir, desktopFileDir, desktopFileName)
	err = os.WriteFile(filePath, desktopFileCopy, desktopFileMod)
	if err != nil {
		return fmt.Errorf("error: write file: %w", err)
	}
	config.ChownFileIfNeeded(filePath)

	return nil
}
