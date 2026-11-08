//go:build !unix

package v1

// setUmask is a no-op where files have no Unix creation mask.
func setUmask(mask int) int { return 0 }
