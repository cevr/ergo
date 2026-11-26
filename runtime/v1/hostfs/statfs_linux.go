//go:build linux

package hostfs

import (
	"math"
	"syscall"
)

// Statfs is syscall.Statfs's capacity metrics for name in bytes.
func Statfs(name string) (StatfsInfo, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(name, &st); err != nil {
		return StatfsInfo{}, err
	}
	bsize := st.Bsize
	if st.Frsize > 0 {
		bsize = st.Frsize
	}
	if bsize <= 0 {
		return StatfsInfo{}, nil
	}
	bs := uint64(bsize)
	total := int64(math.MaxInt64)
	if st.Blocks <= uint64(math.MaxInt64)/bs {
		total = int64(st.Blocks * bs)
	}
	free := int64(math.MaxInt64)
	if st.Bfree <= uint64(math.MaxInt64)/bs {
		free = int64(st.Bfree * bs)
	}
	avail := int64(math.MaxInt64)
	if st.Bavail <= uint64(math.MaxInt64)/bs {
		avail = int64(st.Bavail * bs)
	}
	return StatfsInfo{Total: total, Free: free, Available: avail}, nil
}
