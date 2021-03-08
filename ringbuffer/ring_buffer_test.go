package ringbuffer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRingBuffer_Write_overflow(t *testing.T) {
	rb := New(10)
	data := genBytes(0, 9)

	rb.Write(data)
	real := rb.Bytes()
	assert.Equal(t, data, real)

	rb.Write(genBytes(10, 14))
	real = rb.Bytes()
	assert.Equal(t, genBytes(5, 14), real)

	rb.Write(genBytes(15, 19))
	real = rb.Bytes()
	assert.Equal(t, genBytes(10, 19), real)
}

func TestRingBuffer_Write_small(t *testing.T) {
	rb := New(10)
	real := rb.Bytes()
	assert.Equal(t, make([]byte, 0), real)

	data := genBytes(1, 5)

	rb.Write(data)
	real = rb.Bytes()
	assert.Equal(t, data, real)

	rb.Write(genBytes(6, 7))
	real = rb.Bytes()
	assert.Equal(t, genBytes(1, 7), real)
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
