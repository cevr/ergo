//go:build unix

package v1

import "syscall"

// setUmask sets the process file creation mask and returns the old one.
// No test in this package runs in parallel, so a test may change it for
// its own cases and restore it after.
func setUmask(mask int) int { return syscall.Umask(mask) }
