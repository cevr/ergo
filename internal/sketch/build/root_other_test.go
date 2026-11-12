//go:build !linux

package build

// rootHelpers has no modes here: the test rule reads /proc (root_linux.go).
func rootHelpers(string) (bool, int) { return false, 0 }
