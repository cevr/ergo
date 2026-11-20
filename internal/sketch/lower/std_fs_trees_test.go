package lower

import (
	"strings"
	"testing"
)

// fsTreesLinks walks and removes trees that hold symbolic links, on the
// test host's folder: fs.Walk lists a link and does not go through it, and
// fs.RemoveAll removes a link and never what it leads to, inside the tree
// or outside it.
const fsTreesLinks = `import fs

let onHost = context needs TestAmbient { fs.Files: fs.NewTestHost(), fs.Writes: fs.NewTestHost() }

test WalkListsALinkOnce {
	let h = fs.NewTestHost()
	try fs.MkdirAll(h, "t/d")
	try fs.WriteFile(h, "t/d/f", "x")
	try fs.Symlink(h, "d", "t/link")
	try fs.Symlink(h, "..", "t/d/up")
	with onHost {
		assert try fs.Walk("t") == []string{"t", "t/d", "t/d/f", "t/d/up", "t/link"}
	}
}

test RemoveAllKeepsWhatALinkLeadsTo {
	let h = fs.NewTestHost()
	try fs.MkdirAll(h, "keep")
	try fs.WriteFile(h, "keep/f", "x")
	try fs.MkdirAll(h, "gone/d")
	try fs.Symlink(h, "../keep", "gone/out")
	try fs.Symlink(h, "../../keep", "gone/d/deep")
	with onHost {
		try fs.RemoveAll("gone")
		assert try fs.Files.ReadFile("keep/f") == "x"
		assert try fs.Files.ReadDir(".") == []string{"keep"}
	}
}
`

// TestFSTreesKeepLinks runs fsTreesLinks. The memory provider has no
// links, so TestFSMemoryAgreesWithHost cannot see a walk or a removal
// through one. Mutants it kills (std mutant ledger): a Walk that follows
// a link (fs2-walk-follows: Stat for Lstat lists t/link/f and loops on
// t/d/up), and a RemoveAll that reads a path as a directory before it
// removes it (fs2-removeall-reads-first: it empties keep).
// It builds Go because the functions are Ergo source over the host's
// bodies on a real folder, and no in-process interpreter runs them.
func TestFSTreesKeepLinks(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "fs_trees_links", fsTreesLinks)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["fs_trees_links"], "--seed", "1")
	if code != 0 || !strings.Contains(stdout, "ok") || strings.Contains(stdout, "FAIL") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
}
