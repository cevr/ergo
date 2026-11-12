//go:build !linux

package lint

// limitMemory is unsupported off Linux; the time and output limits still
// bound the pack.
func limitMemory(int, uint64) error { return nil }
