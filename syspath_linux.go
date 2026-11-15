//go:build linux && !android

package awl

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ipfs/go-log/v2"
)

// systemTools are the programs awl and its dependencies run by name: iptables
// for the exit node NAT, resolvconf and systemctl for the DNS setup.
var systemTools = []string{"iptables", "ip6tables", "resolvconf", "systemctl"}

// systemToolDirs are checked in this order for system tools missing from PATH.
// systemd units and pkexec get a fixed FHS PATH, and on NixOS and Guix the
// system tools live outside of it.
var systemToolDirs = []string{
	"/run/current-system/sw/bin",       // NixOS
	"/run/current-system/profile/sbin", // Guix
	"/run/current-system/profile/bin",  // Guix
	"/usr/local/sbin",
	"/usr/sbin",
	"/sbin",
	"/usr/bin",
	"/bin",
}

type appendedDir struct {
	dir   string
	tools []string
}

// appendSystemToolDirsToPath appends to PATH the directories that hold system
// tools missing from it. PATH stays first, so it changes only on systems where
// a tool would not be found otherwise.
func appendSystemToolDirsToPath(logger *log.ZapEventLogger) {
	newPath, appended := appendToolDirs(os.Getenv("PATH"), systemTools, systemToolDirs)
	if len(appended) == 0 {
		return
	}
	if err := os.Setenv("PATH", newPath); err != nil {
		logger.Warnf("set PATH: %v", err)
		return
	}
	for _, a := range appended {
		logger.Infof("PATH: appended %s (%s)", a.dir, strings.Join(a.tools, ", "))
	}
}

// appendToolDirs returns path with the directories from dirs that hold tools
// not found in path. For each tool the first matching directory wins.
func appendToolDirs(path string, tools, dirs []string) (string, []appendedDir) {
	pathDirs := filepath.SplitList(path)
	var appended []appendedDir
	for _, tool := range tools {
		if findToolDir(pathDirs, tool) != "" {
			continue
		}
		dir := findToolDir(dirs, tool)
		if dir == "" {
			continue
		}
		i := slices.IndexFunc(appended, func(a appendedDir) bool { return a.dir == dir })
		if i < 0 {
			appended = append(appended, appendedDir{dir: dir})
			i = len(appended) - 1
		}
		appended[i].tools = append(appended[i].tools, tool)
	}

	for _, a := range appended {
		if path == "" {
			path = a.dir
		} else {
			path += string(filepath.ListSeparator) + a.dir
		}
	}
	return path, appended
}

// findToolDir returns the first directory in dirs with an executable file named
// tool. exec.LookPath with an absolute path checks the file and ignores PATH.
func findToolDir(dirs []string, tool string) string {
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		if _, err := exec.LookPath(filepath.Join(dir, tool)); err == nil {
			return dir
		}
	}
	return ""
}
