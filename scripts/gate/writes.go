package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A closure sweep (sweepStep) fails on any change of the checkout during
// its run that its package's reads meet: a change of a path a test looked
// up, or of a directory it listed. So no step may write inside the
// checkout while a sweep runs. Twice a step that does wrote there while
// the merge tier's uncached run swept, which runs every step at once in a
// fresh clone: git's index under the go command's VCS stamp (batch-9 try3),
// then bin/ergo, which "build bin/ergo" links in a clone that has none
// (try4: the sweeps of internal/bindc, internal/sketchfront and
// scripts/gate/replay failed with "bin/ergo changed during"). The rule, for
// every tier: a step that writes inside the checkout says so (Step.Writes);
// no sweep starts before every such step has ended (sweepsAfterWriters);
// and each sweep fails on any change of the checkout during its run, so a
// writer that does not say so is found on the first run it overlaps a
// sweep, not only when the swept package reads what it wrote
// (sweepCheckout). Ordering costs the sweeps the time from the end of "go
// test build", which they need, to the end of "build bin/ergo", which needs
// it too: about a second (try4 log: 0.8 s).

// sweepsAfterWriters gives, for each step, the steps it starts after
// without needing their pass: for a sweep step, every step that writes the
// checkout. It refuses a set of steps in which a sweep would wait for
// itself: a step that both sweeps and writes, or a writer that needs a
// sweep, directly or through other needs.
func sweepsAfterWriters(steps []*Step, index map[string]int) ([][]int, error) {
	var writers []int
	for i, step := range steps {
		if len(step.Writes) == 0 {
			continue
		}
		if step.Sweep != "" {
			return nil, fmt.Errorf("step %q sweeps the checkout and writes %s in it", step.Name, strings.Join(step.Writes, ", "))
		}
		if sweep := neededSweep(steps, index, i, map[int]bool{}); sweep != "" {
			return nil, fmt.Errorf("step %q writes %s in the checkout and needs the sweep %q, which runs only after every writer", step.Name, strings.Join(step.Writes, ", "), sweep)
		}
		writers = append(writers, i)
	}
	after := make([][]int, len(steps))
	for i, step := range steps {
		if step.Sweep != "" {
			after[i] = writers
		}
	}
	return after, nil
}

// neededSweep names a sweep step that step i needs, through any chain of
// needs, or is "".
func neededSweep(steps []*Step, index map[string]int, i int, seen map[int]bool) string {
	for _, need := range steps[i].Needs {
		j, ok := index[need]
		if !ok || seen[j] {
			continue
		}
		seen[j] = true
		if steps[j].Sweep != "" {
			return steps[j].Name
		}
		if sweep := neededSweep(steps, index, j, seen); sweep != "" {
			return sweep
		}
	}
	return ""
}

// sweepCheckout runs a sweep step and fails it when the checkout changed
// while it ran: a path added, removed or changed in kind, permissions,
// size or modification time (checkoutEntries). A replayed pass ran no
// sweep, so it is not checked.
func (g *gate) sweepCheckout(step *Step) *result {
	before, err := checkoutEntries(g.root)
	if err != nil {
		return &result{outcome: failed, output: fmt.Appendf(nil, "gate: %s: list the checkout: %v\n", step.Name, err)}
	}
	r := g.execute(step)
	if r.outcome == cached {
		return r
	}
	after, err := checkoutEntries(g.root)
	if err != nil {
		r.outcome = failed
		r.output = fmt.Appendf(r.output, "gate: %s: list the checkout: %v\n", step.Name, err)
		return r
	}
	if changed := changedEntries(before, after); len(changed) > 0 {
		r.outcome = failed
		shown := changed
		if len(shown) > 10 {
			shown = shown[:10]
		}
		r.output = fmt.Appendf(r.output, "gate: %s: the checkout changed while the sweep ran (%d paths: %s); a step that writes in the checkout names what it writes in Step.Writes, so that no sweep runs beside it\n", step.Name, len(changed), strings.Join(shown, ", "))
	}
	return r
}

// entryIdentity is what checkoutEntries keeps of one path.
type entryIdentity struct {
	mode fs.FileMode
	size int64
	mod  time.Time
}

// checkoutEntries lists every path of the checkout below root, files Git
// ignores included, with its identity (os.Lstat: links are not followed).
// A directory's modification time changes when an entry is added to it or
// removed, so a file made and removed during a sweep shows too. Of a .git
// directory (a clone's, such as the merge tier's export) it keeps the
// directory and the files directly in it, where a step's git writes the
// index and its lock, and not its subdirectories: a commit in a linked
// worktree of the same repository writes objects and refs there, which no
// step of this checkout made. A path that goes while the list is made is
// left out.
func checkoutEntries(root string) (map[string]entryIdentity, error) {
	// WalkDir does not enter a root that is a link.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	entries := map[string]entryIdentity{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".git/") && d.IsDir() {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		entries[rel] = entryIdentity{mode: info.Mode(), size: info.Size(), mod: info.ModTime()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// changedEntries are the paths whose identity differs between two lists,
// sorted.
func changedEntries(before, after map[string]entryIdentity) []string {
	var changed []string
	for path, identity := range before {
		if other, ok := after[path]; !ok || other.mode != identity.mode || other.size != identity.size || !other.mod.Equal(identity.mod) {
			changed = append(changed, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed
}
