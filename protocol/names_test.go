package protocol

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "laptop", "laptop"},
		{"control characters", "lap\x00top\n\x1b[31m\u0085", "laptop[31m"},
		{"direction overrides and isolates", "\u202Egnp.exe\u202C \u2066x\u2069", "gnp.exe x"},
		{"spaces around", "  laptop\t", "laptop"},
		{"spaces left after dropping", "\u202E laptop \u202C", "laptop"},
		{"emoji joined with U+200D", "👩\u200D💻 laptop", "👩\u200D💻 laptop"},
		{"letters of other scripts", "ноутбук 笔记本", "ноутбук 笔记本"},
		{"long, cut by characters", strings.Repeat("я", MaxNameLength+50), strings.Repeat("я", MaxNameLength)},
		{"no trailing space after the cut", strings.Repeat("a", MaxNameLength-1) + " tail", strings.Repeat("a", MaxNameLength-1)},
		{"nothing left", "\u202E\x00 ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NormalizeName(tc.in))
		})
	}
}

// TestReceiveNormalizesName: a name is normalized as it is received, both in
// an auth request and in a status info.
func TestReceiveNormalizesName(t *testing.T) {
	const sent, want = "\u202Egnp.exe\x07 ", "gnp.exe"

	var buf bytes.Buffer
	require.NoError(t, SendAuth(&buf, AuthPeer{Name: sent}))
	authPeer, err := ReceiveAuth(&buf)
	require.NoError(t, err)
	require.Equal(t, want, authPeer.Name)

	buf.Reset()
	require.NoError(t, SendStatus(&buf, PeerStatusInfo{Name: sent}))
	statusInfo, err := ReceiveStatus(&buf)
	require.NoError(t, err)
	require.Equal(t, want, statusInfo.Name)
}
