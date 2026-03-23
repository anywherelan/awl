package api

import (
	"testing"
)

func Test_byteCountIEC(t *testing.T) {
	cases := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{4 * 1024 * 1024 * 1024, "4.0 GiB"},
	}

	for _, tc := range cases {
		got := byteCountIEC(tc.input)
		if got != tc.want {
			t.Errorf("byteCountIEC(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
