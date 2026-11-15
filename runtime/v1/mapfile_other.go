//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package v1

import (
	"io"
	"os"
)

// fsMap reads size bytes of f into a buffer only FSMapFile holds, where
// the runtime maps no file (windows, js, wasip1): the text is the one a
// mapping gives, a copy is its cost, and no store reaches the buffer, so
// unsafe.String's invariant holds as it does for a read-only mapping. A
// file that shrank under the read fails as a short read.
func fsMap(f *os.File, size int) ([]byte, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}
	return data, nil
}
