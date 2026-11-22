//go:build !linux && !darwin

package v1

import "os"

// physicalPath is path resolved (absResolved): this host has no name for
// a descriptor that the runtime reads.
func physicalPath(_ *os.File, path string) string { return absResolved(path) }
