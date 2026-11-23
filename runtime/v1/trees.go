package v1

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The bodies of std fs's Lstat and Mkdir on the host (Files, Writes), of
// its link functions (fs/trees.ergo). Each is Go's own call; an error
// becomes NotFound, Denied or Failed with the host's reason (fsErr),
// naming the path as the caller gave it.

// Exists is fs.Exists: Mkdir found something at Path already.
type Exists struct{ Path string }

// Error is the failure's text, as NotFound's is.
func (e Exists) Error() string { return "fs.Exists{Path:" + e.Path + "}" }

// FSLstat is the body of fs.Host's Lstat: Stat, but a symbolic link at
// path is described itself, not followed (Go's os.Lstat); a link is not a
// directory.
//
//ergo:host
//ergo:wait none
func FSLstat(path string) (Info, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Info{}, fsErr(path, err)
	}
	return Info{Name: fi.Name(), Size: int(fi.Size()), Dir: fi.IsDir(), Perm: int(fi.Mode().Perm())}, nil
}

// FSMkdir is the body of fs.Host's Mkdir: Go's os.Mkdir, then the exact
// perm, whatever the umask. Anything at path, a link included, is Exists:
// the directory is made only when nothing was there, so two callers never
// both make one name.
//
//ergo:host
//ergo:wait none
func FSMkdir(path string, perm int) error {
	mode := fs.FileMode(perm) & fs.ModePerm
	if err := os.Mkdir(path, mode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Exists{Path: path}
		}
		return fsErr(path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fsErr(path, err)
	}
	return nil
}

// FSIsLink is the body of fs.IsLink: whether path itself, not followed,
// is a symbolic link (Go's os.Lstat).
//
//ergo:host
//ergo:wait none
func FSIsLink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fsErr(path, err)
	}
	return info.Mode()&fs.ModeSymlink != 0, nil
}

// FSReadlink is the body of fs.Readlink: the target of the symbolic link
// at path, as it was written (Go's os.Readlink).
//
//ergo:host
//ergo:wait none
func FSReadlink(path string) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", fsErr(path, err)
	}
	return target, nil
}

// FSSymlink is the body of fs.Symlink: a symbolic link at path whose
// target is target, written as given (Go's os.Symlink).
//
//ergo:host
//ergo:wait none
func FSSymlink(target, path string) error {
	if err := os.Symlink(target, path); err != nil {
		return fsErr(path, err)
	}
	return nil
}

// FSResolve is the body of fs.Resolve: path with every symbolic link in
// it replaced by its target (Go's filepath.EvalSymlinks).
//
//ergo:host
//ergo:wait none
func FSResolve(path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fsErr(path, err)
	}
	return p, nil
}
