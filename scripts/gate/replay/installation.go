package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// InstallationDigest is the fingerprint of an installation tree (GOROOT, a
// tool's file, git's exec path) and of a directory a step walks itself
// (Inputs.Installed): every path below root with its kind, every file's
// keyed permission bits and content, every symlink's target text, following
// links (each directory once, by its resolved path) and hashing files
// concurrently. A link from the tree back into the repository contributes
// only its target: a step whose outcome depends on the files there declares
// them through Paths. An absent root is the distinct digest "absent".
//
// Every path it reaches, a link's resolved target included, passes KindOf:
// a FIFO, a socket or a device below root returns its *Unkeyable, and the
// fingerprint fails, so the facts that would hold it cannot key a replay.
func InstallationDigest(root, repository string) (string, error) {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return "", err
	}
	inside := func(name, dir string) bool {
		relative, err := filepath.Rel(dir, name)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	type file struct{ relative, path string }
	var entries []string
	var files []file
	visited := map[string]bool{}
	var walk func(relative, name string) error
	walk = func(relative, name string) error {
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			entries = append(entries, "link\x00"+relative+"\x00"+target)
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil {
				entries = append(entries, "dangling\x00"+relative)
				return nil
			}
			if inside(resolved, resolvedRepository) && !inside(resolved, resolvedRoot) {
				return nil
			}
			if info, err = os.Stat(name); err != nil {
				return err
			}
		}
		kind, err := KindOf(name, info.Mode())
		switch kind {
		case Directory:
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil {
				return err
			}
			if visited[resolved] {
				entries = append(entries, "seen\x00"+relative)
				return nil
			}
			visited[resolved] = true
			children, err := os.ReadDir(name)
			if err != nil {
				return err
			}
			entries = append(entries, "dir\x00"+relative)
			for _, child := range children {
				if err := walk(path.Join(relative, child.Name()), filepath.Join(name, child.Name())); err != nil {
					return err
				}
			}
		case File:
			entries = append(entries, fmt.Sprintf("file\x00%s\x00%v", relative, info.Mode().Perm()&KeyedPermissions))
			files = append(files, file{relative, name})
		case Other:
			return err
		}
		return nil
	}
	if err := walk(".", root); err != nil {
		return "", err
	}
	sums := make([]string, len(files))
	errs := make([]error, len(files))
	var wg sync.WaitGroup
	work := make(chan int)
	for range max(2, runtime.NumCPU()) {
		wg.Go(func() {
			for index := range work {
				sums[index], errs[index] = fileSum(files[index].path)
			}
		})
	}
	for index := range files {
		work <- index
	}
	close(work)
	wg.Wait()
	hash := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(hash, "%s\x00", entry)
	}
	for index, f := range files {
		if errs[index] != nil {
			return "", errs[index]
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", f.relative, sums[index])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
