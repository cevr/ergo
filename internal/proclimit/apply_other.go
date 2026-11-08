//go:build !linux

package proclimit

// apply does nothing here: see the package comment.
func apply(int, uint64) error { return nil }
