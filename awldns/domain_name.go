package awldns

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Domain names of peers are given without the LocalDomain zone. They are kept
// in Unicode, as the user sees them, and served in their ASCII form (punycode),
// which is what clients ask for: a query name comes to us in presentation
// format, where a byte outside printable ASCII is escaped as \DDD, so a
// Unicode name could never match.

const (
	maxLabelLength = 63
	// A name is at most 253 octets, the zone with its dot included
	maxDomainNameLength = 253 - len(LocalDomain) - 1
	// defaultDomainName is what a name with nothing usable for a domain name gives
	defaultDomainName = "peer"
)

// idnaProfile converts domain names as browsers do (UTS 46, non-transitional),
// but keeps '_', which browsers accept as well and which awl has always used for
// spaces.
var idnaProfile = idna.New(
	idna.MapForLookup(),
	idna.Transitional(false),
	idna.StrictDomainName(false),
	idna.VerifyDNSLength(true),
)

// NormalizeDomainName makes a domain name out of a peer name: it keeps letters,
// digits, '-', '_' and dots, turns spaces into '_', lowercases the name and cuts
// labels that are too long. Dots make a compound name, such as "laptop.home",
// served as laptop.home.awl. A name with nothing usable gives "peer".
func NormalizeDomainName(name string) string {
	return normalizeDomainName(name, "")
}

// NormalizeDomainNameWithSuffix is NormalizeDomainName with suffix appended to
// the first label, so that "laptop.home" gives "laptop_0.home". A label too
// long for the suffix is cut, the suffix is kept.
func NormalizeDomainNameWithSuffix(name, suffix string) string {
	return normalizeDomainName(name, suffix)
}

// IsValidDomainName reports whether domain is already normalized, see
// NormalizeDomainName.
func IsValidDomainName(domain string) bool {
	return domain != "" && NormalizeDomainName(domain) == domain
}

func normalizeDomainName(name, suffix string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return '_'
		// Non-ASCII is left to IDNA, see normalizeLabel
		case r >= utf8.RuneSelf, r == '-', r == '_', r == '.',
			'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
			return r
		}
		return -1
	}, strings.TrimSpace(name))

	var labels []string
	for label := range strings.SplitSeq(name, ".") {
		if label = normalizeLabel(label); label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		labels = []string{defaultDomainName}
	}
	for i := range labels {
		if i == 0 {
			labels[i] = fitLabel(labels[i], suffix)
		} else {
			labels[i] = fitLabel(labels[i], "")
		}
	}

	domain := strings.Join(labels, ".")
	if ascii, err := domainNameToASCII(domain); err != nil || len(ascii) > maxDomainNameLength {
		return defaultDomainName + suffix
	}
	return domain
}

// normalizeLabel lowercases a label and maps it as IDNA does. IDNA rejects '-'
// at either end and "--" (it is reserved for "xn--" and the like), so these are
// dropped first. A label IDNA rejects for anything else gives "".
func normalizeLabel(label string) string {
	for strings.Contains(label, "--") {
		label = strings.ReplaceAll(label, "--", "-")
	}
	label, err := idnaProfile.ToUnicode(strings.Trim(label, "-"))
	if err != nil {
		return ""
	}
	return label
}

// fitLabel appends suffix to label, cutting the label so that its ASCII form
// fits into a DNS label.
func fitLabel(label, suffix string) string {
	runes := []rune(label)
	// The ASCII form is never shorter than the label in characters
	runes = runes[:min(len(runes), maxLabelLength)]
	for {
		fitted := string(runes) + suffix
		if _, err := domainNameToASCII(fitted); err == nil || len(runes) == 0 {
			return fitted
		}
		runes = runes[:len(runes)-1]
	}
}

// domainNameToASCII returns the ASCII form of a domain name, the one clients
// ask for: "ноутбук" is "xn--90asheufc".
func domainNameToASCII(domain string) (string, error) {
	return idnaProfile.ToASCII(domain)
}
