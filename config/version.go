package config

import (
	"runtime"
	"strings"
)

const DevVersion = "dev"

var (
	Version         = DevVersion
	UserAgent       = UserAgentPrefix + SystemInfo + "/" + Version
	UserAgentPrefix = "awl/"
	SystemInfo      = runtime.GOOS + "-" + runtime.GOARCH
)

// IsDevVersion
// Possible duplicate of *Config.DevMode()
// Based on build version (unchangeable after build, could be used only by developers)
func IsDevVersion() bool {
	return Version == DevVersion
}

func VersionFromUserAgent(userAgent string) string {
	i := strings.LastIndex(userAgent, "/")
	if i == -1 || i == len(userAgent)-1 {
		return ""
	}

	return userAgent[i+1:]
}

func SystemInfoFromUserAgent(userAgent string) (goos string, goarch string) {
	userAgent = strings.TrimPrefix(userAgent, UserAgentPrefix)

	systemInfo, _, ok := strings.Cut(userAgent, "/")
	if !ok {
		return
	}
	goos, goarch, _ = strings.Cut(systemInfo, "-")
	return
}
