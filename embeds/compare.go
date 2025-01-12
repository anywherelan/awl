package embeds

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

func checkIsFileEqual(path string, data []byte) bool {
	stat, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	} else if stat.Size() != int64(len(data)) {
		return false
	}

	file, err := os.Open(path)
	if err != nil {
		fmt.Printf("error: read %s file: %v\n", path, err)
		return false
	}
	fileClosed := false
	closeFile := func() {
		if fileClosed {
			return
		}
		err := file.Close()
		fileClosed = true
		if err != nil {
			fmt.Printf("error: close read %s file: %v\n", path, err)
		}
	}
	defer closeFile()

	equal, err := streamsEqual(bytes.NewReader(data), bufio.NewReader(file))
	if err != nil {
		fmt.Printf("error: compare %s files: %v\n", path, err)
	}

	return equal
}

func streamsEqual(s1 io.Reader, s2 io.Reader) (bool, error) {
	const chunkSize = 4096
	buf1 := make([]byte, chunkSize)
	buf2 := make([]byte, chunkSize)
	for {
		len1, err1 := io.ReadFull(s1, buf1)
		len2, err2 := io.ReadFull(s2, buf2)
		if (err1 != nil && err1 != io.ErrUnexpectedEOF) || (err2 != nil && err2 != io.ErrUnexpectedEOF) {
			if (err1 == io.EOF || err1 == nil) && (err2 == io.EOF || err2 == nil) {
				return err1 == io.EOF && err2 == io.EOF, nil
			}
			return false, fmt.Errorf("compare streams reading err: source1 err: %v; source2 err: %v", err1, err2)
		}
		if !bytes.Equal(buf1[:len1], buf2[:len2]) {
			return false, nil
		}
	}
}
