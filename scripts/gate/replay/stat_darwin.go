package replay

import (
	"fmt"
	"io/fs"
	"syscall"
	"time"
)

// statTuple is what the key holds for a stat-keyed path: size, mtime and
// ctime in nanoseconds, inode and mode.
func statTuple(info fs.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Sprintf("unstatted:%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d:%d:%d:%d:%v", info.Size(), st.Mtimespec.Nano(), st.Ctimespec.Nano(), st.Ino, info.Mode())
}

// changedSince reports whether the path's mtime or ctime is after cutoff.
func changedSince(info fs.FileInfo, cutoff time.Time) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	limit := cutoff.UnixNano()
	return st.Mtimespec.Nano() > limit || st.Ctimespec.Nano() > limit
}
