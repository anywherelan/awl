package config

import (
	"strings"
)

const DevVersion = "dev"

var (
	Version         = DevVersion
	UserAgent       = UserAgentPrefix + Version
	UserAgentPrefix = "awl/"
)

// IsDevVersion
// Possible duplicate of *Config.DevMode()
// Based on build version (unchangeable after build, could be used only by developers)
func IsDevVersion() bool {
	return Version == DevVersion
}

func VersionFromUserAgent(userAgent string) string {
	return strings.TrimPrefix(userAgent, UserAgentPrefix)
}
