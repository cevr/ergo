package sketchfront

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitTree is a file system of a commit (`ergo test --affected BASE`,
// CI-SELECT CS-AFFECTED): a path under the repository's top folder reads
// the commit's tree, its entries listed once (git ls-tree) and each file
// read from its blob (git cat-file), with no checkout; any other path
// reads the host, as the disk loader does. The loader admits a commit's
// sources through it as it admits the working tree's through the host
// (hidden folders, testdata, nested modules, links): one admission, so a
// difference between the two is a difference of the sources. A link is
// followed within the tree; one that leaves it reads the host. A
// submodule is an empty folder. Every git call passes
// --no-optional-locks (TestGitCallsTakeNoOptionalLocks). Close ends the one
// git process a tree keeps for its reads.
type GitTree struct {
	top     string
	commit  string
	entries map[string]gitEntry
	// children are each folder's entry names, by its path in the tree
	// ("" for the top).
	children map[string][]string
	host     hostFS

	mu       sync.Mutex
	blobs    map[string][]byte
	batch    *exec.Cmd
	batchIn  io.WriteCloser
	batchOut *bufio.Reader
}

type gitEntry struct {
	mode int64 // octal mode as git writes it: 100644, 100755, 120000, 040000, 160000
	oid  string
	size int64
}

// maxLinks bounds the links one name follows, as the host's ELOOP does.
const maxLinks = 40

// OpenGitTree is the file system of the commit rev names in the
// repository that holds dir.
func OpenGitTree(dir, rev string) (*GitTree, error) {
	out, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	out, err = gitOutput(top, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%s names no commit of %s", rev, top)
	}
	t := &GitTree{top: top, commit: strings.TrimSpace(string(out)), entries: map[string]gitEntry{}, children: map[string][]string{}, blobs: map[string][]byte{}}
	out, err = gitOutput(top, "ls-tree", "-r", "-t", "-l", "-z", "--full-tree", t.commit)
	if err != nil {
		return nil, err
	}
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) != 4 {
			return nil, fmt.Errorf("git ls-tree: unexpected line %q", rec)
		}
		mode, err := strconv.ParseInt(f[0], 8, 64)
		if err != nil {
			return nil, fmt.Errorf("git ls-tree: unexpected mode %q", f[0])
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		p := string(name)
		t.entries[p] = gitEntry{mode: mode, oid: f[2], size: size}
		parent := path.Dir(p)
		if parent == "." {
			parent = ""
		}
		t.children[parent] = append(t.children[parent], path.Base(p))
	}
	return t, nil
}

// Commit is the commit's full object name.
func (t *GitTree) Commit() string { return t.commit }

// Top is the repository's top folder.
func (t *GitTree) Top() string { return t.top }

// rel is name's path in the tree, and whether name is under the top.
func (t *GitTree) rel(name string) (string, bool) {
	r, err := filepath.Rel(t.top, filepath.Clean(name))
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	if r == "." {
		return "", true
	}
	return filepath.ToSlash(r), true
}

const (
	modeTree    = 0o40000
	modeLink    = 0o120000
	modeModule  = 0o160000
	modeExecute = 0o100755
)

// resolve follows the links of the tree path p: the path it names in the
// tree, or, for a link that leaves the tree, the host path (outside).
func (t *GitTree) resolve(p string, follow bool) (rel string, e gitEntry, outside string, err error) {
	for range maxLinks {
		if p == "" {
			return "", gitEntry{mode: modeTree}, "", nil
		}
		// The folders on the way are followed too.
		parent, base := path.Dir(p), path.Base(p)
		if parent == "." {
			parent = ""
		}
		if parent != "" {
			pr, pe, out, err := t.resolve(parent, true)
			if err != nil {
				return "", gitEntry{}, "", err
			}
			if out != "" {
				return "", gitEntry{}, filepath.Join(out, base), nil
			}
			if pe.mode != modeTree {
				return "", gitEntry{}, "", fs.ErrNotExist
			}
			parent = pr
		}
		p = path.Join(parent, base)
		e, ok := t.entries[p]
		if !ok {
			return "", gitEntry{}, "", fs.ErrNotExist
		}
		if e.mode != modeLink || !follow {
			return p, e, "", nil
		}
		target, err := t.blob(e)
		if err != nil {
			return "", gitEntry{}, "", err
		}
		dest := string(target)
		if path.IsAbs(dest) {
			abs := filepath.FromSlash(dest)
			r, in := t.rel(abs)
			if !in {
				return "", gitEntry{}, abs, nil
			}
			p = r
			continue
		}
		joined := path.Join(parent, dest)
		if joined == ".." || strings.HasPrefix(joined, "../") {
			return "", gitEntry{}, filepath.Join(t.top, filepath.FromSlash(joined)), nil
		}
		if joined == "." {
			joined = ""
		}
		p = joined
	}
	return "", gitEntry{}, "", errors.New("too many levels of symbolic links")
}

// blob is the bytes of e's object, read once through one `git cat-file
// --batch` the tree keeps open until Close.
func (t *GitTree) blob(e gitEntry) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if b, ok := t.blobs[e.oid]; ok {
		return b, nil
	}
	if t.batch == nil {
		cmd := exec.Command("git", "--no-optional-locks", "-C", t.top, "cat-file", "--batch")
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		t.batch, t.batchIn, t.batchOut = cmd, in, bufio.NewReader(out)
	}
	if _, err := io.WriteString(t.batchIn, e.oid+"\n"); err != nil {
		return nil, err
	}
	head, err := t.batchOut.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(head)
	if len(f) != 3 {
		return nil, fmt.Errorf("git cat-file: %s", strings.TrimSpace(head))
	}
	size, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %s", strings.TrimSpace(head))
	}
	b := make([]byte, size+1) // the object and its newline
	if _, err := io.ReadFull(t.batchOut, b); err != nil {
		return nil, err
	}
	b = b[:size]
	t.blobs[e.oid] = b
	return b, nil
}

// Close ends the tree's git cat-file; the tree reads nothing after it.
func (t *GitTree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.batch == nil {
		return nil
	}
	t.batchIn.Close()
	err := t.batch.Wait()
	t.batch = nil
	return err
}

func pathErr(op, name string, err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }

// ReadDir lists a folder of the commit.
func (t *GitTree) ReadDir(dir string) ([]fs.DirEntry, error) {
	r, in := t.rel(dir)
	if !in {
		return t.host.ReadDir(dir)
	}
	p, e, out, err := t.resolve(r, true)
	if err != nil {
		return nil, pathErr("readdir", dir, err)
	}
	if out != "" {
		return t.host.ReadDir(out)
	}
	if e.mode == modeModule {
		return nil, nil
	}
	if e.mode != modeTree {
		return nil, pathErr("readdir", dir, errors.New("not a directory"))
	}
	var list []fs.DirEntry
	for _, name := range t.children[p] {
		list = append(list, fs.FileInfoToDirEntry(t.info(name, t.entries[path.Join(p, name)])))
	}
	return list, nil
}

// ReadFile reads a file of the commit, following links.
func (t *GitTree) ReadFile(name string) ([]byte, error) {
	r, in := t.rel(name)
	if !in {
		return t.host.ReadFile(name)
	}
	_, e, out, err := t.resolve(r, true)
	if err != nil {
		return nil, pathErr("open", name, err)
	}
	if out != "" {
		return t.host.ReadFile(out)
	}
	if e.mode == modeTree || e.mode == modeModule {
		return nil, pathErr("read", name, errors.New("is a directory"))
	}
	return t.blob(e)
}

// Stat describes a file of the commit, following links.
func (t *GitTree) Stat(name string) (fs.FileInfo, error) { return t.stat(name, true) }

// Lstat describes a file of the commit, not following a last link.
func (t *GitTree) Lstat(name string) (fs.FileInfo, error) { return t.stat(name, false) }

func (t *GitTree) stat(name string, follow bool) (fs.FileInfo, error) {
	r, in := t.rel(name)
	if !in {
		if follow {
			return t.host.Stat(name)
		}
		return t.host.Lstat(name)
	}
	_, e, out, err := t.resolve(r, follow)
	if err != nil {
		return nil, pathErr("stat", name, err)
	}
	if out != "" {
		return t.host.Stat(out)
	}
	return t.info(filepath.Base(name), e), nil
}

func (t *GitTree) info(name string, e gitEntry) fs.FileInfo { return gitInfo{name: name, e: e} }

type gitInfo struct {
	name string
	e    gitEntry
}

func (i gitInfo) Name() string { return i.name }
func (i gitInfo) Size() int64  { return i.e.size }
func (i gitInfo) Mode() fs.FileMode {
	switch i.e.mode {
	case modeTree, modeModule:
		return fs.ModeDir | 0o755
	case modeLink:
		return fs.ModeSymlink | 0o777
	case modeExecute:
		return 0o755
	}
	return 0o644
}
func (i gitInfo) ModTime() time.Time { return time.Time{} }
func (i gitInfo) IsDir() bool        { return i.Mode().IsDir() }
func (i gitInfo) Sys() any           { return nil }

// DefaultBase is the commit `--affected` compares with when given no
// BASE: the merge base of HEAD and the default branch (origin's HEAD,
// else main, else master), as Turborepo's --affected and the article's
// test:changed take it.
func DefaultBase(dir string) (string, error) {
	var candidates []string
	if out, err := gitOutput(dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(out)))
	}
	candidates = append(candidates, "main", "master")
	for _, c := range candidates {
		if _, err := gitOutput(dir, "rev-parse", "--verify", "--quiet", c+"^{commit}"); err != nil {
			continue
		}
		out, err := gitOutput(dir, "merge-base", "HEAD", c)
		if err != nil {
			return "", fmt.Errorf("no merge base of HEAD and %s", c)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", errors.New("no default branch (origin/HEAD, main or master) to take the merge base with: name BASE")
}

// gitOutput runs git in dir and returns its standard output.
func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
