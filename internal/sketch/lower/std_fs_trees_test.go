package lower

import (
	"strings"
	"testing"
)

// fsTreesLinks walks and removes trees that hold symbolic links, on the
// test host's folder: fs.Walk lists a link and does not go through it, and
// fs.RemoveAll removes a link and never what it leads to, inside the tree
// or outside it.
const fsTreesLinks = `import filepath
import fs
import fstest
import strings

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

// The confined Host acts on a link itself at the last element of a path,
// as Go's Lstat, Remove and Rename do, and still confines: the directory
// above is resolved and refused when it leads out, and an operation that
// follows (ReadDir, ReadFile) through a kept link that leads out is
// refused.

test RemoveOfALinkKeepsItsTarget {
	with onHost {
		try fs.Writes.MkdirAll("keep")
		try fs.Writes.WriteFile("keep/f", "x")
		try Links.Link("keep", "l")
		try fs.Writes.Remove("l")
		assert try fs.Files.ReadFile("keep/f") == "x"
		assert try fs.Files.ReadDir(".") == []string{"keep"}
	}
}

test RenameOfALinkMovesTheLink {
	with onHost {
		try fs.Writes.MkdirAll("keep")
		try fs.Writes.WriteFile("keep/f", "x")
		try Links.Link("keep", "l")
		try fs.Writes.Rename("l", "m")
		assert try fs.Files.ReadDir(".") == []string{"keep", "m"}
		assert try fs.Files.ReadDir("keep") == []string{"f"}
		let moved = try fs.Files.Lstat("m")
		assert !moved.Dir
		assert try fs.Files.ReadFile("m/f") == "x"
	}
}

test LstatOfALinkUpIsTheLink {
	with onHost {
		try fs.Writes.MkdirAll("t/d")
		try Links.Link("..", "t/d/up")
		let l = try fs.Files.Lstat("t/d/up")
		let s = try fs.Files.Stat("t/d/up")
		assert !l.Dir && s.Dir
	}
}

test LstatOfALinkOutIsTheLink {
	with onHost {
		try Links.Link("..", "out")
		let l = try fs.Files.Lstat("out")
		assert !l.Dir
	}
}

test LstatBelowALinkOutIsDenied {
	with onHost {
		try Links.Link("..", "out")
		let _ = fs.Files.Lstat("out/x") catch {
		case fs.Denied:
			return
		case fs.NotFound, fs.Failed:
			assert false
			return
		}
		assert false
	}
}

test RemoveBelowALinkOutIsDenied {
	with onHost {
		try Links.Link("..", "out")
		fs.Writes.Remove("out/x") catch {
		case fs.Denied:
			return
		case fs.NotFound, fs.Failed:
			assert false
			return
		}
		assert false
	}
}

test ReadDirThroughALinkOutIsDenied {
	with onHost {
		try Links.Link("..", "out")
		let _ = fs.Files.ReadDir("out") catch {
		case fs.Denied:
			return
		case fs.NotFound, fs.Failed:
			assert false
			return
		}
		assert false
	}
}

// walkRef is the reference walk: path, then each entry's walk in ReadDir's
// order, by recursion.
func walkRef(path string) []string fails fs.NotFound | fs.Denied | fs.Failed needs fs.Files {
	let info = try fs.Files.Lstat(path)
	if !info.Dir {
		return []string{path}
	}
	let mut out = []string{path}
	for name in try fs.Files.ReadDir(path) {
		for q in try walkRef(path + "/" + name) {
			out = out.Append(q)
		}
	}
	out
}

test WalkIsTheRecursiveWalk(paths []string) {
	let m = fstest.NewMemory()
	for p in paths {
		if p == "" || p == "." || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			continue
		}
		m.MkdirAll(filepath.Dir("t/" + p)) catch {
		case fs.Denied, fs.Failed: continue
		}
		m.WriteFile("t/" + p, "x") catch {
		case fs.NotFound, fs.Denied, fs.Failed: continue
		}
	}
	m.MkdirAll("t/z/y") catch {
	case fs.Denied, fs.Failed: return
	}
	with context { fs.Files: m } {
		assert try fs.Walk("t") == try walkRef("t")
	}
}
`

// TestFSTreesKeepLinks runs fsTreesLinks. The memory provider has no
// links, so TestFSMemoryAgreesWithHost cannot see a walk or a removal
// through one. Mutants it kills (std mutant ledger): a Walk that follows
// a link (fs2-walk-follows: Stat for Lstat lists t/link/f and loops on
// t/d/up), and a RemoveAll that reads a path as a directory before it
// removes it (fs2-removeall-reads-first: it empties keep); a Walk that
// pushes a directory's entries in order (fs2-walk-push-forward:
// WalkIsTheRecursiveWalk); and in the runtime ledger, a test Host that
// resolves the last element (fs2-confinelast-follows: Remove, Rename and
// Lstat of a link) or skips the check of the directory above
// (fs2-confinelast-noparent: the Denied tests).
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
