package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/scripts/gate/bodyedit"
	"ergo.local/prototype/scripts/gate/replay"
	"ergo.local/prototype/scripts/mutants/covfile"
)

// The round tier (CI-SELECT CS-ROUND): `./scripts/gate.sh --affected
// [BASE]` is the fast tier's graph with every test of each package, full
// mode (no -short, no size cut), narrowed to the tests that the change
// BASE..worktree reaches by the mutant runner's coverage store
// (scripts/mutants/covfile): the segment of the package's suite that the
// store's index names at BASE. A test is skipped only when the segment
// knows it, it passed there, and the change touches neither a block it ran
// of a package the segment covers nor a file it read. A change the
// segment cannot place runs every test of the package (fail closed), and
// says why: no segment at BASE, another toolchain or mode, a file that is
// no function body's lines (a declaration, a test file, a file added or
// removed, an embedded or data file, a declared read, go.mod), a Go file
// of a package the segment does not cover.
//
// It is sound because the segment is the coverage of BASE's tree (the
// index names a commit only for a clean checkout whose suite key holds):
// a test that passed at BASE and reads nothing the change touches gives
// the same verdict on the worktree. The round tier is advisory for a lane
// and never a landing verdict: the full, merge and Mac tiers run every
// test on every batch head (NORTH_STAR Owner rules).
const roundTier = "round"

// roundSelectionVariable set to "full" turns the round tier's selection
// off: every test of every package runs (the back-out switch).
const roundSelectionVariable = "ERGO_GATE_SELECTION"

// roundBase is the commit the round tier selects against (main's
// --affected argument, else the merge base with main).
var roundBase string

// maxSkipBytes bounds the -skip list of a round step: past it the step
// runs every test of its package (an argument is at most 128 KiB on Linux).
const maxSkipBytes = 64 << 10

// fileChange is one file of the change: its content at the base and now;
// a missing side is nil with its flag false.
type fileChange struct {
	Path     string
	Old, New []byte
	Was, Is  bool
}

// roundPick is the round tier's outcome for one package: every test with
// a reason (all), or the tests that run, each with the reason.
type roundPick struct {
	all string
	run map[string]string
}

// pickRound chooses the tests of a package that the changes reach, by the
// package's closure (what its tests read, children included, as the
// closure sweep checks it) and its segment at the base (entry and seg).
// names are the tests the package declares now.
func pickRound(entry covfile.Entry, seg *covfile.Coverage, closure *replay.Closure, changes []fileChange, names []string) roundPick {
	all := func(reason string) roundPick { return roundPick{all: reason} }
	if entry.Mode != covfile.Mode {
		return all(fmt.Sprintf("the segment at the base is of mode %q, not a full run", entry.Mode))
	}
	run := map[string]string{}
	mark := func(test int, reason string) {
		id := seg.Tests[test].ID
		name := id[strings.LastIndex(id, ":")+1:]
		if _, ok := run[name]; !ok {
			run[name] = reason
		}
	}
	cover := map[string]bool{}
	for _, p := range entry.Cover {
		cover[strings.TrimPrefix(p, "./")] = true
	}
	for _, c := range changes {
		if !closure.Covers(c.Path) {
			continue
		}
		// The tests that opened the file read every line of it.
		for _, b := range seg.Files[c.Path] {
			if b.Start == covfile.ReadBlock {
				for _, t := range b.Tests {
					mark(t, "reads "+c.Path)
				}
			}
		}
		dir, base := path.Dir(c.Path), path.Base(c.Path)
		switch {
		case strings.HasSuffix(base, "_test.go") && dir != closure.Dir:
			continue // another package's test: not in this test binary
		case !slices.Contains(closure.Deps, dir) || !strings.HasSuffix(base, ".go"):
			return all(c.Path + " changed: an embedded, data or declared input the tests or a program they start may read")
		case strings.HasSuffix(base, "_test.go"):
			return all(c.Path + " changed: a test file")
		case !c.Was || !c.Is:
			return all(c.Path + " was added or removed: the binary is built from it")
		case !cover[dir]:
			return all(c.Path + " changed: the segment records no block of " + dir)
		}
		lines, reason := bodyedit.BodyLines(string(c.Old), string(c.New))
		if reason != "" {
			return all(c.Path + ": " + reason + " (a declaration)")
		}
		for _, b := range seg.Files[c.Path] {
			if b.Start == covfile.ReadBlock {
				continue
			}
			for _, span := range lines {
				if b.Start <= span[1] && b.End >= span[0] {
					for _, t := range b.Tests {
						mark(t, fmt.Sprintf("blocks %s:%d-%d", c.Path, b.Start, b.End))
					}
				}
			}
		}
	}
	known := map[string]*covfile.Test{}
	for _, t := range seg.Tests {
		known[t.ID[strings.LastIndex(t.ID, ":")+1:]] = t
	}
	for _, name := range names {
		switch t := known[name]; {
		case t == nil:
			run[name] = "new: the segment does not know it"
		case t.Failed:
			run[name] = "failed at the base"
		}
	}
	return roundPick{run: run}
}

// selection is the round pick as the fast tier's selection: the skipped
// tests of names and the lines the gate prints, one per selected test.
func (r roundPick) selection(pkg string, names []string) selection {
	if r.all != "" {
		return selection{note: fmt.Sprintf("gate: %s: every test: %s", pkg, r.all)}
	}
	var skip, kept []string
	counts := map[string]int{}
	for _, name := range names {
		reason, ok := r.run[name]
		if !ok {
			skip = append(skip, name)
			continue
		}
		kept = append(kept, fmt.Sprintf("  %s: %s", name, reason))
		counts[strings.Fields(reason)[0]]++
	}
	if len(exactNames(skip)) > maxSkipBytes {
		return selection{note: fmt.Sprintf("gate: %s: every test: the skip list of %d tests is too long for one argument", pkg, len(skip))}
	}
	var parts []string
	for _, kind := range []string{"blocks", "reads", "new:", "failed"} {
		if counts[kind] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", strings.TrimSuffix(kind, ":"), counts[kind]))
		}
	}
	note := fmt.Sprintf("gate: %s: %d of %d tests (%s)", pkg, len(kept), len(names), strings.Join(parts, ", "))
	if len(kept) > 0 {
		note += "\n" + strings.Join(kept, "\n")
	}
	return selection{skip: skip, note: note}
}

// roundSelector reads the store's index at base once and the change
// base..worktree, and judges each package by its segment there.
func roundSelector(root, base string) (selector, error) {
	if os.Getenv(roundSelectionVariable) == "full" {
		return func(p testPackage, env, names []string) selection {
			return selection{note: fmt.Sprintf("gate: %s: every test: %s=full", p.dir, roundSelectionVariable)}
		}, nil
	}
	commit, err := gitText(root, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("--affected %s: %w", base, err)
	}
	store, err := covfile.Open()
	if err != nil {
		return nil, err
	}
	entries, err := store.At(commit)
	if err != nil {
		return nil, err
	}
	changes, err := changesSince(root, commit)
	if err != nil {
		return nil, err
	}
	// The environment the mutant runner measured under: this process's,
	// without GOROOT (scripts/mutants goEnviron).
	goEnv, goErr := covfile.GoEnv("go", root, slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GOROOT=") }))
	return func(p testPackage, env, names []string) selection {
		all := func(reason string) selection {
			return selection{note: fmt.Sprintf("gate: %s: every test: %s", p.dir, reason)}
		}
		entry, ok := entryOf(entries, p.dir)
		switch {
		case !ok:
			return all(fmt.Sprintf("the coverage store has no segment of it at %s (go run ./scripts/mutants -measure at that commit)", short(commit)))
		case goErr != nil:
			return all(goErr.Error())
		case entry.GoEnv != goEnv:
			return all("the toolchain or the go command's settings differ from the segment's")
		}
		seg, ok := store.Load(entry.Key)
		if !ok {
			return all("the index names a segment the store does not hold")
		}
		return pickRound(entry, seg, p.closure, changes, names).selection(p.dir, names)
	}, nil
}

// entryOf is the index entry of the Go suite of the package in dir built
// from the checkout's module without tags: of several, the one with the
// most named tests is not known here, so the first by suite name.
func entryOf(entries []covfile.Entry, dir string) (covfile.Entry, bool) {
	for _, e := range entries {
		if e.Package == "./"+dir && e.Module == "" && e.Tags == "" {
			return e, true
		}
	}
	return covfile.Entry{}, false
}

// changesSince is the change from commit to the worktree: every tracked
// file that differs (staged or not) and every untracked file not ignored.
func changesSince(root, commit string) ([]fileChange, error) {
	diff, err := gitText(root, "diff", "--name-only", "-z", "--no-renames", commit, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := gitText(root, "ls-files", "-z", "-o", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, list := range []string{diff, untracked} {
		for _, name := range strings.Split(list, "\x00") {
			if name != "" {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	var out []fileChange
	for _, name := range slices.Compact(names) {
		c := fileChange{Path: name}
		show := exec.Command("git", "--no-optional-locks", "cat-file", "blob", commit+":"+name)
		show.Dir = root
		if old, err := show.Output(); err == nil {
			c.Old, c.Was = old, true
		}
		if now, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))); err == nil {
			c.New, c.Is = now, true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if c.Was && c.Is && bytes.Equal(c.Old, c.New) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// gitText runs git in root and returns its output, trimmed of the final
// newline.
func gitText(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	command.Dir = root
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// backtestVariable names the commit TestSelectionHoldsEveryLedgerKiller
// backtests at, instead of the nearest indexed ancestor of HEAD.
const backtestVariable = "ERGO_GATE_BACKTEST_COMMIT"

// backtestCommit is the commit of the store's index nearest to HEAD among
// its ancestors (or backtestVariable's), or an error that says how to
// fill the store: an empty store is no backtest (NO TESTS IS RED).
func backtestCommit(store covfile.Store, root string) (string, error) {
	commits, err := store.Commits()
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("no coverage store at %s: run go run ./scripts/mutants -measure", store.Dir)
	}
	if c := os.Getenv(backtestVariable); c != "" {
		return gitText(root, "rev-parse", "--verify", c+"^{commit}")
	}
	best, distance := "", -1
	for _, c := range commits {
		count, err := gitText(root, "rev-list", "--count", c+"..HEAD")
		if err != nil {
			continue // a commit this checkout does not hold
		}
		if _, err := gitText(root, "merge-base", "--is-ancestor", c, "HEAD"); err != nil {
			continue
		}
		if n, _ := strconv.Atoi(count); distance < 0 || n < distance {
			best, distance = c, n
		}
	}
	if best == "" {
		return "", fmt.Errorf("the coverage store at %s indexes no ancestor of HEAD: run go run ./scripts/mutants -measure on one", store.Dir)
	}
	return best, nil
}

// defaultRoundBase is the merge base of HEAD with main.
func defaultRoundBase(root string) (string, error) {
	return gitText(root, "merge-base", "HEAD", "main")
}

func short(commit string) string {
	if len(commit) > 9 {
		return commit[:9]
	}
	return commit
}
