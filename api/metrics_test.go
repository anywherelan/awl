package api

import (
	"testing"
)

func Test_convertBytesToIECUnits(t *testing.T) {
	tCases := []struct {
		input  float64
		output string
	}{
		{
			input:  0,
			output: "0 B",
		},
		{
			input:  1023,
			output: "1023 B",
		},
		{
			input:  1024,
			output: "1 KiB",
		},
		{
			input:  1148,
			output: "1.12 KiB",
		},
		{
			input:  1153434,
			output: "1.1 MiB",
		},
		{
			input:  4 << 30,
			output: "4 GiB",
		},
	}

	for _, tCase := range tCases {
		if result := convertBytesToIECUnits(tCase.input); result != tCase.output {
			t.Errorf("convertBytesToUnits() got = %s, want %s", result, tCase.output)
		}
	}
}
