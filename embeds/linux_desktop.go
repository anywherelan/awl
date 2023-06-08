package embeds

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anywherelan/ts-dns/util/lineread"

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
	homeDir := os.Getenv("HOME")
	if len(homeDir) == 0 {
		return errors.New("error: empty home dir")
	}

	ex, err := os.Executable()
	if err != nil {
		return fmt.Errorf("error: find executable path: %w", err)
	}

	replacements := map[string][]byte{
		"{VERSION}":  []byte(config.Version),
		"{ICONPATH}": []byte(iconPath),
		"{EXECPATH}": []byte(ex),
	}

	filePath := filepath.Join(homeDir, desktopFileDir, desktopFileName)

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error: create file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Printf("error: close file %v\n", err)
		}
	}()

	err = lineread.Reader(bytes.NewReader(desktopFile), func(line []byte) error {
		for replName, replVal := range replacements {
			line = bytes.ReplaceAll(line, []byte(replName), replVal)
		}

		_, err := file.Write(append(line, []byte("\n")...))
		return err
	})
	if err != nil {
		return fmt.Errorf("error: write file: %w", err)
	}

	if config.LinuxFilesOwnerUID == 0 {
		return errors.New("error: user uid is unknown")
	}
	err = file.Chown(config.LinuxFilesOwnerUID, config.LinuxFilesOwnerUID)
	if err != nil {
		return fmt.Errorf("error: chown file: %w", err)
	}
	err = file.Chmod(desktopFileMod)
	if err != nil {
		return fmt.Errorf("error: chmod file: %w", err)
	}

	return nil
}
