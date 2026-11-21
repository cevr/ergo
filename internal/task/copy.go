package task

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scratchPrefix starts the name of the folder a Check copy lives in, at
// the root's .git when there is one (so no status or walk sees it), else
// in the system's temporary folder.
const scratchPrefix = ".ergo-task-copy"

// scratchDir makes the folder a copy of the tree at root goes in.
func scratchDir(root string) (string, func(), error) {
	parent := filepath.Join(root, ".git")
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		parent = ""
	}
	dir, err := os.MkdirTemp(parent, scratchPrefix)
	if err != nil {
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// copyTree copies the tree at src into the empty folder dst, leaving out
// the skipped paths; a file is cloned where the file system shares blocks
// (FICLONE), else copied. Links are copied as links.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skipped(filepath.ToSlash(rel)) || strings.HasPrefix(d.Name(), scratchPrefix) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.Mkdir(to, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case info.Mode().IsRegular():
			return copyFile(p, to, info.Mode().Perm())
		}
		return nil
	})
}

func copyFile(from, to string, perm fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if !cloneFile(out, in) {
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
	}
	return out.Close()
}
