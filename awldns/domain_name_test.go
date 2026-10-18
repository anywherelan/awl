package awldns

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeDomainName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "laptop", "laptop"},
		{"case", "LapTop", "laptop"},
		{"spaces", " my  laptop\t", "my__laptop"},
		{"underscore", "my_laptop", "my_laptop"},
		{"ASCII outside a hostname", `la!p/t"o'p@;()\`, "laptop"},
		{"cyrillic and its case", "Ноутбук", "ноутбук"},
		{"accents composed", "cafe\u0301", "caf\u00e9"},
		{"compound", "Laptop.Home", "laptop.home"},
		{"empty labels", ".laptop..home.", "laptop.home"},
		{"label of nothing usable", "laptop.!!!.home", "laptop.home"},
		{"hyphens at the ends of labels", "-laptop-.-home-", "laptop.home"},
		{"double hyphen", "my--laptop", "my-laptop"},
		// "--" goes before IDNA sees it, so a punycode name is not decoded
		{"punycode in a name", "xn--90asheufc", "xn-90asheufc"},
		{"long ASCII label", strings.Repeat("a", 100), strings.Repeat("a", maxLabelLength)},
		// The longest that fits: the punycode of 57 "я" is 63 characters
		{"long cyrillic label", strings.Repeat("я", 100), strings.Repeat("я", 57)},
		{"every label cut", strings.Repeat("a", 70) + "." + strings.Repeat("b", 70),
			strings.Repeat("a", maxLabelLength) + "." + strings.Repeat("b", maxLabelLength)},
		{"nothing usable", "!!!", defaultDomainName},
		{"empty", "", defaultDomainName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeDomainName(tc.in)
			require.Equal(t, tc.want, got)
			require.True(t, IsValidDomainName(got), "a normalized name is valid")
			ascii, err := domainNameToASCII(got)
			require.NoError(t, err)
			for label := range strings.SplitSeq(ascii, ".") {
				require.LessOrEqual(t, len(label), maxLabelLength)
			}
		})
	}
}

func TestNormalizeDomainNameWithSuffix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "laptop", "laptop_0"},
		{"compound, to the first label", "laptop.home", "laptop_0.home"},
		{"long label is cut, not the suffix", strings.Repeat("a", 100), strings.Repeat("a", maxLabelLength-2) + "_0"},
		{"nothing usable", "!!!", defaultDomainName + "_0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeDomainNameWithSuffix(tc.in, "_0")
			require.Equal(t, tc.want, got)
			require.True(t, IsValidDomainName(got))
		})
	}
}

func TestIsValidDomainName(t *testing.T) {
	for _, domain := range []string{"laptop", "my_laptop", "laptop.home", "ноутбук", "café"} {
		require.True(t, IsValidDomainName(domain), domain)
	}
	for _, domain := range []string{"", "Laptop", "НОУТБУК", "a!b", "-laptop", "laptop.", "a..b",
		"my laptop", "xn--90asheufc", strings.Repeat("a", maxLabelLength+1)} {
		require.False(t, IsValidDomainName(domain), domain)
	}
}
