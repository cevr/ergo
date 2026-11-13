//go:build !unix

package main

import "math"

// statFree has no file system query here: every path has room.
func statFree(string) (uint64, error) { return math.MaxUint64, nil }
