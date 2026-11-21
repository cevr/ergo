//go:build !linux

package main

// reapHelper: the reaper's helpers are Linux's (reap_linux_test.go).
func reapHelper() bool { return false }
