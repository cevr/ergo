//go:build !linux

package sketchfront

// statFile gives no stat off Linux, so StatSums hashes every file.
func statFile(name string) (FileStat, bool) { return FileStat{}, false }
