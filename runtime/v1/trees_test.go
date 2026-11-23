package v1

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// TestFSSymlinkReadsBack: a link fs.Symlink made reads back, through
// fs.Readlink, its target exactly as written, and fs.IsLink sees it. The
// targets are generated paths with repeated and trailing separators, ".",
// ".." and the empty one, which need not exist. Mutants it kills (runtime
// mutant ledger): a Readlink that cleans the target (fs2-readlink-cleans),
// an IsLink that follows the link (fs2-islink-follows).
func TestFSSymlinkReadsBack(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 2))
	dir := t.TempDir()
	names := []string{"a", "b", "c/d"}
	made := 0
	for range lawCases / 10 {
		target := genPath(r)
		path := filepath.Join(dir, names[r.IntN(len(names))])
		if err := FSSymlink(target, path); err != nil {
			continue
		}
		made++
		got, err := FSReadlink(path)
		isLink, err2 := FSIsLink(path)
		if err != nil || err2 != nil || got != target || !isLink {
			t.Fatalf("Symlink(%q, %q): Readlink %q, %v; IsLink %v, %v", target, path, got, err, isLink, err2)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	// c/d never takes a link (c is missing), and the empty target is
	// refused, so about half the cases make one.
	if made < lawCases/40 {
		t.Errorf("a link was made in only %d cases", made)
	}
}
