//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package build

import (
	"path/filepath"
	"testing"
	"time"
)

// TestNothingIsTrimmedWithoutLocks (required test 9): without flock, a
// build with no stamp leaves an entry unused for two days, and staging two
// hours old.
func TestNothingIsTrimmedWithoutLocks(t *testing.T) {
	root := t.TempDir()
	dir := publishBase(t, root)
	ageBy(t, dir, 2*unusedLimit)
	staging := filepath.Join(root, "staging", "key.123")
	mustWrite(t, filepath.Join(staging, "main.go"), "package main\n")
	ageBy(t, staging, 2*stagingLifetime)
	dropTrimStamp(t, root)
	Cache{Root: root}.maybeTrim(time.Now())
	if !there(dir) || !there(staging) {
		t.Fatalf("a build without flock removed: entry present %v, staging present %v", there(dir), there(staging))
	}
}
