package ringbuffer

import (
	"sync"
)

const (
	fillingRate = 17 // from 10
)

// RingBuffer is a circular buffer that implement io.Writer interface.
type RingBuffer struct {
	buf  []byte
	size int
	pos  int
	mu   sync.Mutex
}

// New returns a new RingBuffer whose buffer has the given size.
func New(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]byte, size*2),
		size: size,
	}
}

// Write writes len(p) bytes from p to the underlying buf.
func (r *RingBuffer) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()

	if r.pos > r.size*fillingRate/10 {
		s := r.pos - r.size
		copy(r.buf[:r.size], r.buf[s:r.pos])
		r.pos = r.size
	}

	n = copy(r.buf[r.pos:], p)
	r.pos += n

	r.mu.Unlock()

	return n, err
}

// Capacity returns the size of the underlying buffer.
func (r *RingBuffer) Capacity() int {
	return r.size
}

// Bytes returns all available read bytes.
func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	lastIndex := r.pos
	if lastIndex == 0 {
		return make([]byte, 0)
	}

	firstIndex := lastIndex - r.size
	if firstIndex < 0 {
		firstIndex = 0
	}
	buf := make([]byte, lastIndex-firstIndex)
	copy(buf, r.buf[firstIndex:lastIndex])

	return buf
}

// Reset the read pointer and writer pointer to zero.
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.pos = 0
}
