//go:build !linux

package main

import "os"

// outputDestination admits every file off Linux: the RSS watch bounds only
// the steps' resident memory there, not memory charged to the gate, and
// darwin's temp and cache directories are on disk.
func outputDestination(*os.File) error {
	return nil
}
