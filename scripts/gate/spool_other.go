//go:build !linux

package main

// memoryFileSystem is false off Linux: the RSS watch bounds only the steps'
// resident memory there, and darwin's temp and cache directories are on disk.
func memoryFileSystem(string) (bool, error) {
	return false, nil
}
