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

// Links makes symbolic links in the case's folder: fs.Symlink takes the
// Host, which only a context that needs TestAmbient may make.
type Links {
	Link(target string, path string) fails fs.NotFound | fs.Denied | fs.Failed,
}

type hostLinks { h fs.Host } implements Links

func Link(l hostLinks, target string, path string) fails fs.NotFound | fs.Denied | fs.Failed {
	try fs.Symlink(l.h, target, path)
}

let onHost = context needs TestAmbient { fs.Files: fs.NewTestHost(), fs.Writes: fs.NewTestHost(), Links: hostLinks{h: fs.NewTestHost()} }

test WalkListsALinkOnce {
	with onHost {
		try fs.Writes.MkdirAll("t/d")
		try fs.Writes.WriteFile("t/d/f", "x")
		try Links.Link("d", "t/link")
		try Links.Link("..", "t/d/up")
		assert try fs.Walk("t") == []string{"t", "t/d", "t/d/f", "t/d/up", "t/link"}
	}
}

test RemoveAllKeepsWhatALinkLeadsTo {
	with onHost {
		try fs.Writes.MkdirAll("keep")
		try fs.Writes.WriteFile("keep/f", "x")
		try fs.Writes.MkdirAll("gone/d")
		try Links.Link("../keep", "gone/out")
		try Links.Link("../../keep", "gone/d/deep")
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
