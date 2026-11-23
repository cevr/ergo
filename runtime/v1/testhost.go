package v1

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The test's ambient (docs/spec/rows.md, Test's ambient; runtime.md, Tests 11).
// The test runner gives each build of a context that needs
// TestAmbient, a with of it in a test or one case of a law run under
// it, a fresh folder (TestFolderOpen) and removes it when the case ends
// (TestFolderRemove). The confined providers (fs.TestHost, exec.TestHost,
// os.TestHost) resolve every path they are given against that folder
// (Confine) and refuse one that leads out of it with their Denied
// failure, never a panic.

// TestFolderOpen makes a fresh empty folder under the test's temporary
// directory (TMPDIR, or the host's default) and returns its absolute path
// with its symbolic links resolved, so a path the providers resolve is
// compared with it as is. A folder that cannot be made is a defect of the
// runner: no case can run without one.
//
//ergo:host
func TestFolderOpen() string {
	dir, err := os.MkdirTemp("", "ergo-case-")
	if err != nil {
		panic("ergo test: the case's folder: " + err.Error())
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		panic("ergo test: the case's folder: " + err.Error())
	}
	return real
}

// testFolderEnds is the leaves' work at the end of a case's folder: exec's
// ends the folder's sandbox helper (runtime/v1/exec exec_sandbox_linux.go).
// The core imports no leaf (BS8), so a leaf adds its own in its init
// (OnTestFolderRemove), before any task runs.
var testFolderEnds []func(dir string)

// OnTestFolderRemove adds end to the work TestFolderRemove does before it
// removes a folder. It is called only from a leaf's init.
func OnTestFolderRemove(end func(dir string)) { testFolderEnds = append(testFolderEnds, end) }

// TestFolderRemove ends the case's sandbox helper, if a command of the
// folder started one (testFolderEnds), and removes the case's
// folder and all it holds. A read-only directory a case made is made
// writable first, so the removal does not stop at it. A folder that
// cannot be removed is a defect of the runner: the next case must not see
// this one's files.
//
//ergo:host
func TestFolderRemove(dir string) {
	for _, end := range testFolderEnds {
		end(dir)
	}
	err := os.RemoveAll(dir)
	if err != nil {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0o700)
			}
			return nil
		})
		err = os.RemoveAll(dir)
	}
	if err != nil {
		panic("ergo test: removing the case's folder: " + err.Error())
	}
}

// Confine resolves path for a provider confined to root, an absolute
// folder with its links resolved: a relative path is relative to base
// (root itself when base is ""), an absolute one is itself. The path is
// cleaned lexically, then the longest prefix of it that exists has its
// symbolic links resolved, so a link inside root that points out of it
// leads out. It reports the resolved path and whether it lies in root
// (root itself included).
//
//ergo:host
func Confine(root, base, path string) (string, bool) {
	if base == "" {
		base = root
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	// The longest prefix that exists, its links resolved; the rest is
	// lexical and holds no "..", since p is clean.
	rest := ""
	head := p
	for {
		real, err := filepath.EvalSymlinks(head)
		if err == nil {
			p = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(head)
		if parent == head {
			break
		}
		rest = filepath.Join(filepath.Base(head), rest)
		head = parent
	}
	return p, inFolder(root, p)
}

// inFolder reports whether p, a clean absolute path, is root or below it.
func inFolder(root, p string) bool {
	if p == root {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep)
}

// FSConfine is the body of fs.TestHost's path check: path resolved in the
// folder root, or fs.Denied of path when it leads out of it. The empty
// path stays empty, so the operation fails on it as the host's does.
//
//ergo:host
//ergo:wait none
func FSConfine(root string, path string) (string, error) {
	if path == "" {
		return "", nil // the host's own failure of the empty path
	}
	p, ok := Confine(root, "", path)
	if !ok {
		return "", Denied{Path: path}
	}
	return p, nil
}

// FSConfineLast is FSConfine for an operation on path itself, which Go
// does not follow at the last element (Lstat, Remove, Rename, Readlink,
// Symlink): the directory above path is resolved and confined, and path's
// last element is joined to it as written, so a link there is the link and
// not what it leads to. The folder itself is FSConfine's.
//
//ergo:host
//ergo:wait none
func FSConfineLast(root string, path string) (string, error) {
	if path == "" {
		return "", nil // the host's own failure of the empty path
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if p == root || !strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
		return FSConfine(root, path)
	}
	dir, ok := Confine(root, "", filepath.Dir(p))
	if !ok {
		return "", Denied{Path: path}
	}
	return filepath.Join(dir, filepath.Base(p)), nil
}

// TestSeed is the run's --seed, as the test binary's harness reads it
// (lower/tests.go, $Seed): the value of --seed N or --seed=N among the
// process's arguments, 1 when none is given or it is no decimal uint64
// (the harness has already refused that run). A harnessed test's rewrite
// calls it for its run's seed (check harness.go, rule 3), so the harness
// needs no generated Go of its own.
func TestSeed() uint64 { return testSeed() }

var testSeed = sync.OnceValue(func() uint64 { return seedOf(os.Args[1:]) })

// TestSource is the source path a harnessed test's replay command names
// (check harness.go, rule 3): the value of --source among the process's
// arguments, the file or package folder ergo test was given, or file, the
// test's own, when the binary runs alone (lower/tests.go, $Source).
func TestSource(file string) string {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		key, v, joined := strings.Cut(args[i], "=")
		if !joined {
			if i+1 == len(args) {
				break
			}
			i++
			v = args[i]
		}
		if key == "--source" {
			return v
		}
	}
	return file
}

func seedOf(args []string) uint64 {
	for i := 0; i < len(args); i++ {
		key, v, joined := strings.Cut(args[i], "=")
		if !joined {
			if i+1 == len(args) {
				break
			}
			i++
			v = args[i]
		}
		if key == "--seed" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				return n
			}
			return 1
		}
	}
	return 1
}
