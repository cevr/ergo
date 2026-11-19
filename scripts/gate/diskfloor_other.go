//go:build !(linux || darwin || freebsd)

package main

import "math"

// statFree has no file system query here (syscall.Statfs_t differs on
// netbsd and openbsd, and is absent elsewhere): every path has room.
func statFree(string) (uint64, error) { return math.MaxUint64, nil }
