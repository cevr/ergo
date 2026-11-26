//go:build linux

package stdlib_test

import (
	"math"
	"syscall"
)

func rawStatfs(path string) (total, free, avail int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bsize := st.Bsize
	if st.Frsize > 0 {
		bsize = st.Frsize
	}
	if bsize <= 0 {
		return 0, 0, 0, nil
	}
	bs := uint64(bsize)
	scale := func(blocks uint64) int64 {
		if blocks > uint64(math.MaxInt64)/bs {
			return math.MaxInt64
		}
		return int64(blocks * bs)
	}
	return scale(st.Blocks), scale(st.Bfree), scale(st.Bavail), nil
}
