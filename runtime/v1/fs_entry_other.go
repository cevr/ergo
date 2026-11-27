//go:build !linux && !openbsd && !dragonfly && !darwin && !freebsd && !netbsd

package v1

import "io/fs"

func fillFSEntryStat(_ fs.FileInfo, _ *FSEntry) {}
