package protocol

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameLength is the longest name a peer can give itself, in characters, as
// the API allows it.
const MaxNameLength = 100

// IsUnsafeNameRune reports whether r must not be part of a name shown to the
// user: a control character, or one that changes the direction of the text
// around it (U+202A–U+202E, U+2066–U+2069) and so can make a name read as
// another one. The rest of the format characters are fine: U+200D, for one,
// joins emoji.
func IsUnsafeNameRune(r rune) bool {
	return unicode.IsControl(r) ||
		(r >= '\u202A' && r <= '\u202E') ||
		(r >= '\u2066' && r <= '\u2069')
}

// NormalizeName makes a name that came from a peer safe to show: it drops
// unsafe characters, trims spaces and cuts the name to MaxNameLength.
func NormalizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if IsUnsafeNameRune(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxNameLength {
		name = strings.TrimSpace(string([]rune(name)[:MaxNameLength]))
	}

	return name
}
