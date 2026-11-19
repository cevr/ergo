package build

import (
	"path/filepath"
	"testing"
	"time"
)

// TestResultHitMarksItsEntry: a result hit marks its module's results used,
// so a package whose runs only replay keeps its results from the trim; a
// trim removes unused results. It kills results-unmarked, which no CLI test
// can see: their runs are minutes apart, not unusedLimit.
func TestResultHitMarksItsEntry(t *testing.T) {
	needLocks(t)
	root := t.TempDir()
	c := Cache{Root: root}
	m := Module{Key: "k"}
	key := "0123456789abcdef"
	if _, ok := c.Result(m, key); ok {
		t.Fatal("a result before any was stored")
	}
	if err := c.PutResult(m, key, []byte("out")); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "results", "k")
	ageBy(t, entry, 2*unusedLimit)
	if data, ok := c.Result(m, key); !ok || string(data) != "out" {
		t.Fatalf("result %q, %v", data, ok)
	}
	if p := c.trim(time.Now(), 0); !p.complete || !there(filepath.Join(entry, key)) {
		t.Fatalf("the trim removed results that were just used (pass %+v)", p)
	}
	ageBy(t, entry, 2*unusedLimit)
	if p := c.trim(time.Now(), 0); !p.complete || there(entry) {
		t.Fatalf("the trim kept unused results (pass %+v)", p)
	}
}
