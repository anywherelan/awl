package embeds

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/anywherelan/awl/config"
)

const (
	iconDirPathLinux = ".local/share/icons"
	iconDirWindows   = config.AppDataDirectory
	iconName         = "anywherelan.png"

	dirMode  = 0700
	iconMode = 0664
)

var (
	//go:embed Icon.png
	appIcon []byte

	appIconPath string

	isTempIcon bool
)

func GetIcon() []byte {
	return appIcon
}

func GetIconPath() string {
	return appIconPath
}

func EmbedIcon() (string, error) {
	var (
		iconDir string
		err     error
	)
	switch runtime.GOOS {
	case "linux":
		iconDir, err = getIconDirLinux()
	case "windows":
		iconDir, err = getIconDirWindows()
	default:
		iconDir = getIconDirDefault()
		isTempIcon = true
	}

	if err != nil {
		return "", err
	}

	err = os.Mkdir(iconDir, dirMode)
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("error: create dir: %w", err)
	}
	config.ChownFileIfNeeded(iconDir)

	iconPath := filepath.Join(iconDir, iconName)
	equal := checkIsFileEqual(iconPath, appIcon)
	if equal {
		appIconPath = iconPath
		return iconDir, nil
	}

	err = os.WriteFile(iconPath, appIcon, iconMode)
	if err != nil {
		return "", fmt.Errorf("error: write file: %w", err)
	}
	config.ChownFileIfNeeded(iconPath)

	appIconPath = iconPath

	return iconDir, nil
}

func RemoveIconIfNeeded() error {
	if len(appIconPath) == 0 {
		return nil
	}

	if !isTempIcon {
		return nil
	}

	return os.Remove(appIconPath)
}

func getIconDirLinux() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("error: get home dir: %w", err)
	}
	iconDir := filepath.Join(homeDir, iconDirPathLinux)

	return iconDir, nil
}

func getIconDirWindows() (string, error) {
	appDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("error: get user config dir: %w", err)
	}
	iconDir := filepath.Join(appDir, iconDirWindows)

	return iconDir, nil
}

func getIconDirDefault() string {
	return os.TempDir()
}
