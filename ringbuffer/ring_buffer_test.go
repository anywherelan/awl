package ringbuffer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRingBuffer_Write_overflow(t *testing.T) {
	rb := New(10)
	data := genBytes(0, 9)

	_, _ = rb.Write(data)
	realBytes := rb.Bytes()
	assert.Equal(t, data, realBytes)

	_, _ = rb.Write(genBytes(10, 14))
	realBytes = rb.Bytes()
	assert.Equal(t, genBytes(5, 14), realBytes)

	_, _ = rb.Write(genBytes(15, 19))
	realBytes = rb.Bytes()
	assert.Equal(t, genBytes(10, 19), realBytes)
}

func TestRingBuffer_Write_small(t *testing.T) {
	rb := New(10)
	realBytes := rb.Bytes()
	assert.Equal(t, make([]byte, 0), realBytes)

	data := genBytes(1, 5)

	_, _ = rb.Write(data)
	realBytes = rb.Bytes()
	assert.Equal(t, data, realBytes)

	_, _ = rb.Write(genBytes(6, 7))
	realBytes = rb.Bytes()
	assert.Equal(t, genBytes(1, 7), realBytes)
}

func genBytes(from, to int) []byte {
	l := to - from + 1
	data := make([]byte, l)

	i := 0
	for val := from; val <= to; val++ {
		data[i] = byte(val)
		i++
	}

	return data
}
