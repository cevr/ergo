package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/task"
)

// gitBase is a task.Base read from Git: the commit base of the
// repository whose top folder is root, as `git diff` and `git ls-tree`
// give it, with no checkout.
type gitBase struct {
	root    string
	changed map[string]bool     // paths that differ, untracked ones too
	under   map[string]bool     // folders holding a changed path (each ancestor)
	names   map[string][]string // the base's names below each folder: "f" or "d" then the name
}

// readGitBase reads base's tree and the working tree's difference from it.
func readGitBase(root, base string) (*gitBase, error) {
	git := func(args ...string) ([]string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }), nil
	}
	top, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, fmt.Errorf("--affected reads Git: %s is in no repository", root)
	}
	if t, _ := filepath.EvalSymlinks(strings.TrimSpace(string(top))); t != mustEval(root) {
		return nil, fmt.Errorf("--affected reads Git from the repository's top folder: run it at %s", strings.TrimSpace(string(top)))
	}
	b := &gitBase{root: root, changed: map[string]bool{}, under: map[string]bool{}, names: map[string][]string{}}
	diff, err := git("diff", "--name-only", "--no-renames", "-z", base, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, p := range append(diff, untracked...) {
		b.changed[p] = true
		for d := path.Dir(p); ; d = path.Dir(d) {
			b.under[d] = true
			if d == "." {
				break
			}
		}
	}
	files, err := git("ls-tree", "-r", "-z", "--name-only", "--full-tree", base)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, f := range files {
		for p, mark := f, "f"; p != "."; p, mark = path.Dir(p), "d" {
			entry := path.Dir(p) + "\x00" + mark + path.Base(p)
			if seen[entry] {
				break
			}
			seen[entry] = true
			b.names[path.Dir(p)] = append(b.names[path.Dir(p)], mark+path.Base(p))
		}
	}
	return b, nil
}

func mustEval(p string) string {
	if e, err := filepath.EvalSymlinks(p); err == nil {
		return e
	}
	return p
}

func (b *gitBase) Changed(p string) bool { return b.changed[p] }

// Listing is dir's names at the base: those base's tree holds, and the
// working tree's names Git does not see (ignored files), unless a changed
// path lies under one (then the diff added it).
func (b *gitBase) Listing(dir string) (string, error) {
	var entries []task.Entry
	have := map[string]bool{}
	for _, n := range b.names[dir] {
		have[n[1:]] = true
		entries = append(entries, task.Entry{Name: n[1:], Dir: n[0] == 'd'})
	}
	now, err := os.ReadDir(filepath.Join(b.root, filepath.FromSlash(dir)))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, e := range now {
		p := path.Join(dir, e.Name())
		if have[e.Name()] || b.changed[p] || b.under[p] {
			continue
		}
		entries = append(entries, task.Entry{Name: e.Name(), Dir: e.IsDir()})
	}
	if len(entries) == 0 {
		// Git holds no empty folder: the base has none here.
		return task.Missing, nil
	}
	slices.SortFunc(entries, func(x, y task.Entry) int { return strings.Compare(x.Name, y.Name) })
	return task.ListingDigest(entries), nil
}
