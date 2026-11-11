package vpn

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMTU(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured int
		want       int
		wantOK     bool
	}{
		{name: "unset uses default", configured: 0, want: InterfaceMTU, wantOK: true},
		{name: "min", configured: MinInterfaceMTU, want: MinInterfaceMTU, wantOK: true},
		{name: "typical ethernet", configured: 1500, want: 1500, wantOK: true},
		{name: "max", configured: InterfaceMTU, want: InterfaceMTU, wantOK: true},
		{name: "below min", configured: MinInterfaceMTU - 1, want: InterfaceMTU, wantOK: false},
		{name: "negative", configured: -1, want: InterfaceMTU, wantOK: false},
		{name: "above max", configured: InterfaceMTU + 1, want: InterfaceMTU, wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveMTU(tc.configured)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.wantOK, ok)
		})
	}
}
