//go:build !linux && !darwin

package replay

import (
	"fmt"
	"io/fs"
	"time"
)

// statTuple is what the key holds for a stat-keyed path where the gate
// reads no ctime or inode (it runs on Linux and Darwin, stat_linux.go and
// stat_darwin.go): size, mtime and mode, so an edit that keeps all three
// is missed here.
func statTuple(info fs.FileInfo) string {
	return fmt.Sprintf("%d:%d:%v", info.Size(), info.ModTime().UnixNano(), info.Mode())
}

// changedSince reports whether the path's mtime is after cutoff.
func changedSince(info fs.FileInfo, cutoff time.Time) bool {
	return info.ModTime().After(cutoff)
}
