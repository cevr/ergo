package v1

import (
	"os"
	"path/filepath"
	"strings"
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

// TestFolderRemove removes the case's folder and all it holds. A
// read-only directory a case made is made writable first, so the removal
// does not stop at it. A folder that cannot be removed is a defect of the
// runner: the next case must not see this one's files.
//
//ergo:host
func TestFolderRemove(dir string) {
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

// ExecConfine is the body of exec.TestHost's command check: the command's
// directory resolved in the folder root (root for ""), or exec.Denied of
// the first of dir or an argument that leads out of it. An argument is
// read as a path relative to the directory: a flag or a word stays inside,
// and "/", "..", or a path past a link out of the folder does not. What
// the program computes for itself is not seen.
//
//ergo:host
func ExecConfine(root string, dir string, args []string) (string, error) {
	d, ok := Confine(root, "", dir)
	if !ok {
		return "", ExecDenied{Name: dir}
	}
	for _, a := range args {
		if _, ok := Confine(root, d, a); !ok {
			return "", ExecDenied{Name: a}
		}
	}
	return d, nil
}

// ExecConfineSink is a file sink's path resolved in the folder root (a
// relative one is relative to the folder, as the process's own directory
// is for Host), or exec.Denied of it. A sink that is no file has the empty
// path, which stays empty.
//
//ergo:host
func ExecConfineSink(root string, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	p, ok := Confine(root, "", path)
	if !ok {
		return "", ExecDenied{Name: path}
	}
	return p, nil
}
