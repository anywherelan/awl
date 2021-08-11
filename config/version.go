package config

import (
	"strings"
)

var (
	Version         = "dev"
	UserAgent       = UserAgentPrefix + Version
	UserAgentPrefix = "awl/"
)

func VersionFromUserAgent(userAgent string) string {
	return strings.TrimPrefix(userAgent, UserAgentPrefix)
}
