//go:build ignore

package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"flag"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed release-description-template.md
var releaseDescriptionTemplate string

func main() {
	var buildPath string
	flag.StringVar(&buildPath, "build_path", "build", "directory with build files")
	flag.Parse()

	files, err := os.ReadDir(buildPath)
	if err != nil {
		panic(err)
	}
	var awlAndroid string
	var awlLinux []string
	var awlWindows []string
	var awlWindows7 []string
	var awlFreebsd []string
	var awlTrayLinux []string
	var awlTrayWindows []string
	var awlTrayWindows7 []string
	var awlTrayMacos []string
	var checksums []checksum

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		filename := file.Name()

		switch {
		case strings.HasPrefix(filename, "awl-android"):
			awlAndroid = filename
		case strings.HasPrefix(filename, "awl-linux"):
			awlLinux = append(awlLinux, filename)
		case strings.HasPrefix(filename, "awl-windows7"):
			awlWindows7 = append(awlWindows7, filename)
		case strings.HasPrefix(filename, "awl-windows"):
			awlWindows = append(awlWindows, filename)
		case strings.HasPrefix(filename, "awl-freebsd"):
			awlFreebsd = append(awlFreebsd, filename)
		case strings.HasPrefix(filename, "awl-tray-linux"):
			awlTrayLinux = append(awlTrayLinux, filename)
		case strings.HasPrefix(filename, "awl-tray-windows7"):
			awlTrayWindows7 = append(awlTrayWindows7, filename)
		case strings.HasPrefix(filename, "awl-tray-windows"):
			awlTrayWindows = append(awlTrayWindows, filename)
		case strings.HasPrefix(filename, "awl-tray-macos"):
			awlTrayMacos = append(awlTrayMacos, filename)
		default:
			// not a release asset, skip it from downloads and checksums
			continue
		}

		hash, err := sha256File(filepath.Join(buildPath, filename))
		if err != nil {
			panic(err)
		}
		checksums = append(checksums, checksum{Name: filename, Hash: hash})
	}

	sort.Strings(awlLinux)
	sort.Strings(awlWindows)
	sort.Strings(awlWindows7)
	sort.Strings(awlFreebsd)
	sort.Strings(awlTrayLinux)
	sort.Strings(awlTrayWindows)
	sort.Strings(awlTrayWindows7)
	sort.Strings(awlTrayMacos)
	sort.Slice(checksums, func(i, j int) bool {
		return checksums[i].Name < checksums[j].Name
	})

	releaseTag := strings.TrimPrefix(awlAndroid, "awl-android-")
	releaseTag = strings.TrimSuffix(releaseTag, ".apk")

	temp, err := template.New("release-description").Parse(releaseDescriptionTemplate)
	if err != nil {
		panic(err)
	}

	data := map[string]any{
		"ReleaseTag":      releaseTag,
		"AwlAndroid":      awlAndroid,
		"AwlLinux":        awlLinux,
		"AwlWindows":      awlWindows,
		"AwlWindows7":     awlWindows7,
		"AwlFreebsd":      awlFreebsd,
		"AwlTrayLinux":    awlTrayLinux,
		"AwlTrayWindows":  awlTrayWindows,
		"AwlTrayWindows7": awlTrayWindows7,
		"AwlTrayMacos":    awlTrayMacos,
		"Checksums":       checksums,
	}

	err = temp.Execute(os.Stdout, data)
	if err != nil {
		panic(err)
	}
}

type checksum struct {
	Name string
	Hash string
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
