//go:build ignore

package main

import (
	_ "embed"
	"flag"
	"html/template"
	"os"
	"sort"
	"strings"
)

//go:embed release-description-template.md
var releaseDescriptionTemplate string

func main() {
	var buildPath string
	flag.StringVar(&buildPath, "build_path", "build", "directory with build files")

	files, err := os.ReadDir(buildPath)
	if err != nil {
		panic(err)
	}
	var awlAndroid string
	var awlLinux []string
	var awlWindows []string
	var awlWindows7 []string
	var awlTrayLinux []string
	var awlTrayWindows []string
	var awlTrayWindows7 []string
	var awlTrayMacos []string

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
		case strings.HasPrefix(filename, "awl-tray-linux"):
			awlTrayLinux = append(awlTrayLinux, filename)
		case strings.HasPrefix(filename, "awl-tray-windows7"):
			awlTrayWindows7 = append(awlTrayWindows7, filename)
		case strings.HasPrefix(filename, "awl-tray-windows"):
			awlTrayWindows = append(awlTrayWindows, filename)
		case strings.HasPrefix(filename, "awl-tray-macos"):
			awlTrayMacos = append(awlTrayMacos, filename)
		}
	}

	sort.Strings(awlLinux)
	sort.Strings(awlWindows)
	sort.Strings(awlWindows7)
	sort.Strings(awlTrayLinux)
	sort.Strings(awlTrayWindows)
	sort.Strings(awlTrayWindows7)
	sort.Strings(awlTrayMacos)

	releaseTag := strings.TrimPrefix(awlAndroid, "awl-android-")
	releaseTag = strings.TrimSuffix(releaseTag, ".apk")

	temp, err := template.New("release-description").Parse(releaseDescriptionTemplate)
	if err != nil {
		panic(err)
	}

	data := map[string]interface{}{
		"ReleaseTag":      releaseTag,
		"AwlAndroid":      awlAndroid,
		"AwlLinux":        awlLinux,
		"AwlWindows":      awlWindows,
		"AwlWindows7":     awlWindows7,
		"AwlTrayLinux":    awlTrayLinux,
		"AwlTrayWindows":  awlTrayWindows,
		"AwlTrayWindows7": awlTrayWindows7,
		"AwlTrayMacos":    awlTrayMacos,
	}

	err = temp.Execute(os.Stdout, data)
	if err != nil {
		panic(err)
	}
}
