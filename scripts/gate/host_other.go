//go:build !linux

package main

import "runtime"

// readHostFacts names only the operating system off Linux: no test here
// reads a file system type, cgroups or a systemd scope.
func readHostFacts(string) string { return runtime.GOOS }
