package libs

import (
	"bytes"
	"fmt"
	"io"
)

func StreamsEqual(s1 io.Reader, s2 io.Reader) (bool, error) {
	const chunkSize = 64000
	b1 := make([]byte, chunkSize)
	b2 := make([]byte, chunkSize)
	for {
		_, err1 := s1.Read(b1)
		_, err2 := s2.Read(b2)
		if err1 != nil || err2 != nil {
			if err1 == io.EOF && err2 == io.EOF {
				return true, nil
			} else if err1 == io.EOF || err2 == io.EOF {
				return false, nil
			} else {
				return false, fmt.Errorf("compare steams reading err: source1 err: %v; source2 err: %v", err1, err2)
			}
		}
		if !bytes.Equal(b1, b2) {
			return false, nil
		}
		b1 = b1[:0]
		b2 = b2[:0]
	}
}
