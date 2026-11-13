package build

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResultHitMarksItsEntry: a result hit marks its executable entry
// used, as a link does, so a run that only replays results keeps its entry
// from the trim; a trim removes an unused entry with its results. It kills
// results-unmarked, which TestTestResultCache cannot see: its runs are
// minutes apart, not unusedLimit.
func TestResultHitMarksItsEntry(t *testing.T) {
	needLocks(t)
	root := t.TempDir()
	c := Cache{Root: root}
	m := Module{Key: "k"}
	tc := hostToolchain()
	key := "0123456789abcdef"
	if _, ok := c.Result(m, tc, key); ok {
		t.Fatal("a result before any was stored")
	}
	entry := filepath.Join(root, "bins", "k-"+executableVariant(tc))
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.PutResult(m, tc, key, []byte("out")); err != nil {
		t.Fatal(err)
	}
	ageBy(t, entry, 2*unusedLimit)
	if data, ok := c.Result(m, tc, key); !ok || string(data) != "out" {
		t.Fatalf("result %q, %v", data, ok)
	}
	if p := c.trim(time.Now(), 0); !p.complete || !there(filepath.Join(entry, "results", key)) {
		t.Fatalf("the trim removed an entry whose result was just used (pass %+v)", p)
	}
	ageBy(t, entry, 2*unusedLimit)
	if p := c.trim(time.Now(), 0); !p.complete || there(entry) {
		t.Fatalf("the trim kept an unused entry (pass %+v)", p)
	}
}
