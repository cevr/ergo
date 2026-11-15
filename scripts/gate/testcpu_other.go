//go:build !linux

package main

import "errors"

// processCPU reads /proc, which only Linux has: -record-durations -cpu
// runs on Linux.
func processCPU(int) (float64, error) {
	return 0, errors.New("-record-durations -cpu reads /proc: Linux only")
}
