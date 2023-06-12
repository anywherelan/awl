//go:build windows
// +build windows

package embeds

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anywherelan/awl/config"
)

const (
	iconDir     = config.AppDataDirectory
	awlIconName = "Anywherelan.png"

	dirMod  = 0700
	iconMod = 0664
)

var (
	//go:embed Icon.png
	appIcon []byte

	appIconPath string
)

func EmbedIcon() (string, error) {
	appDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("error: get user config dir: %w", err)
	}

	dirPath := filepath.Join(appDir, iconDir)
	err = os.Mkdir(dirPath, dirMod)
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("error: create dir: %w", err)
	}
	config.ChownFileIfNeeded(dirPath)

	iconPath := filepath.Join(dirPath, awlIconName)

	err = os.WriteFile(iconPath, appIcon, iconMod)
	if err != nil {
		return "", fmt.Errorf("error: write file: %w", err)
	}
	config.ChownFileIfNeeded(iconPath)

	appIconPath = iconPath

	return iconPath, nil
}

func GetIcon() []byte {
	return appIcon
}

func GetIconPath() string {
	return appIconPath
}
