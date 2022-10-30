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

func IsDevVersion() bool {
	return Version == DevVersion
}

func VersionFromUserAgent(userAgent string) string {
	return strings.TrimPrefix(userAgent, UserAgentPrefix)
}
