//go:build !linux && !darwin

package replay

import (
	"fmt"
	"io/fs"
	"time"
)

// statTuple has no ctime or inode here, so it never matches: the gate does
// not replay on such a host.
func statTuple(info fs.FileInfo) string {
	return fmt.Sprintf("unstatted:%d", time.Now().UnixNano())
}

func changedSince(fs.FileInfo, time.Time) bool { return true }
