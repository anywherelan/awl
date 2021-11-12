package libs

import (
	"bytes"
	"testing"
)

type compareTest struct {
	i1     interface{}
	i2     interface{}
	result bool
}

func streamsEqualByBytesSlice(tests []compareTest, t *testing.T) {
	for i, test := range tests {
		r1 := bytes.NewReader(test.i1.([]byte))
		r2 := bytes.NewReader(test.i2.([]byte))
		res, err := StreamsEqual(r1, r2)
		if err != nil {
			t.Error(err)
		}
		if res != test.result {
			t.Errorf("stream equals test %d failed with result: %t", i+1, res)
		}
	}
}

func TestStreamsEqual1(t *testing.T) {
	compareTests := []compareTest{
		{
			i1:     []byte("pymq is the greatest developer"),
			i2:     []byte("pymq is the greatest developer"),
			result: true,
		},
		{
			i1:     []byte("cat is the greatest cat"),
			i2:     []byte("pymq is average cat"),
			result: false,
		},
	}
	streamsEqualByBytesSlice(compareTests, t)
}

func TestStreamsEqual2(t *testing.T) {
	compareTests := []compareTest{
		{
			i1:     []byte("Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua"),
			i2:     []byte("Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua"),
			result: true,
		},
		{
			i1:     []byte("Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua"),
			i2:     []byte("Dorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua"),
			result: false,
		},
	}
	streamsEqualByBytesSlice(compareTests, t)
}
