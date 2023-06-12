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

	dirMod  = 0700
	iconMod = 0664
)

var (
	//go:embed Icon.png
	appIcon []byte

	appIconPath string
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
	}

	if err != nil {
		return "", err
	}

	err = os.Mkdir(iconDir, dirMod)
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("error: create dir: %w", err)
	}
	config.ChownFileIfNeeded(iconDir)

	iconPath := filepath.Join(iconDir, iconName)
	err = os.WriteFile(iconPath, appIcon, iconMod)
	if err != nil {
		return "", fmt.Errorf("error: write file: %w", err)
	}
	config.ChownFileIfNeeded(iconDir)

	appIconPath = iconPath

	return iconPath, nil
}

func RemoveIconIfNeeded() error {
	if len(appIconPath) == 0 {
		return nil
	}

	switch runtime.GOOS {
	case "linux":
	case "windows":
	default:
		return os.Remove(appIconPath)
	}

	return nil
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
