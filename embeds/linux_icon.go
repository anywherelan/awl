package embeds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anywherelan/awl/config"
)

const (
	awlDirPath  = ".local/share/Anywherelan"
	awlIconName = "Anywherelan.png"

	dirMod  = 0700
	iconMod = 0664
)

func EmbedIcon(iconBytes []byte) (string, error) {
	homeDir := os.Getenv("HOME")
	if len(homeDir) == 0 {
		return "", errors.New("error: empty home dir")
	}

	dirPath := filepath.Join(homeDir, awlDirPath)
	err := os.Mkdir(dirPath, dirMod)
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("error: create dir: %w", err)
	}
	err = os.Chown(dirPath, config.LinuxFilesOwnerUID, config.LinuxFilesOwnerUID)
	if err != nil {
		return "", fmt.Errorf("error: chown dir: %w", err)
	}

	iconPath := filepath.Join(dirPath, awlIconName)
	file, err := os.Create(iconPath)
	if err != nil {
		return "", fmt.Errorf("error: create file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Printf("error: close file %v\n", err)
		}
	}()

	_, err = file.Write(iconBytes)
	if err != nil {
		return "", fmt.Errorf("error: write file: %w", err)
	}

	err = file.Chown(config.LinuxFilesOwnerUID, config.LinuxFilesOwnerUID)
	if err != nil {
		return "", fmt.Errorf("error: chown file: %w", err)
	}
	err = file.Chmod(iconMod)
	if err != nil {
		return "", fmt.Errorf("error: chmod file: %w", err)
	}

	return iconPath, nil
}
