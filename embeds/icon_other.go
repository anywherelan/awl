//go:build !linux
// +build !linux

package embeds

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

var (
	//go:embed Icon.png
	appIcon []byte

	// TODO: reuse icon on disk between runs. Put it to data dir?
	tempIconFilepath = filepath.Join(os.TempDir(), "awl-icon.png")
)

func EmbedIcon(iconBytes []byte) (string, error) {
	err := os.WriteFile(tempIconFilepath, appIcon, 0666)
	if err != nil {
		return "", fmt.Errorf("error: write file")
	}

	return tempIconFilepath, nil
}

func GetIcon() []byte {
	return appIcon
}

func GetIconPath() string {
	return tempIconFilepath
}
