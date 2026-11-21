package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"ergo.local/prototype/scripts/mutants/covfile"
)

// Blame (CI-SELECT CS-BLAME): `go run ./scripts/gate -blame DIR:TEST
// GOOD..BAD` lists the first-parent tips of the range whose change reaches
// the test by the coverage store's record, each with the reason, for BULK
// MODE's bisect: a red the batch gate found is checked at those tips, not
// at every tip. It lists, it does not pick: a red caused by two tips
// together has both in the list. Tips the record places precisely (a
// block, a read) come first, then those it reaches only by failing closed
// (every test of the package), each group in range order.

// blameTip is one tip of the range: its id and its change, judged against
// the record (tipChanges).
type blameTip struct {
	ID      string
	Changes []fileChange
}

// blameHit is a tip that reaches the test, and why.
type blameHit struct {
	ID, Reason string
	Precise    bool
}

// blame is every tip whose change, by pick, reaches test.
func blame(tips []blameTip, test string, pick func([]fileChange) roundPick) []blameHit {
	var precise, closed []blameHit
	for _, tip := range tips {
		p := pick(tip.Changes)
		if p.all != "" {
			closed = append(closed, blameHit{ID: tip.ID, Reason: "every test: " + p.all})
			continue
		}
		if reason, ok := p.run[test]; ok {
			precise = append(precise, blameHit{ID: tip.ID, Reason: reason, Precise: true})
		}
	}
	return append(precise, closed...)
}

// tipChanges is the change of one tip (parent to tip) as the record can
// place it: a file the parent holds as the record's commit held it changes
// from that content; a file that differs between the record and the
// parent changes from the record's content to the tip's, a wider change
// that stays sound because the record is the coverage of its commit's
// tree. paths are the files the tip changed; each getter gives a file's
// content at its commit, or false.
func tipChanges(paths []string, record, parent, tip func(string) ([]byte, bool)) []fileChange {
	var out []fileChange
	for _, p := range paths {
		was, inRecord := record(p)
		before, inParent := parent(p)
		now, is := tip(p)
		c := fileChange{Path: p, New: now, Is: is}
		if inRecord == inParent && bytes.Equal(was, before) {
			c.Old, c.Was = before, inParent
		} else {
			c.Old, c.Was = was, inRecord
		}
		if c.Was == c.Is && bytes.Equal(c.Old, c.New) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// runBlame is -blame: it returns the exit code.
func runBlame(target, span string, out io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(out, "gate:", err)
		return 2
	}
	dir, test, ok := strings.Cut(target, ":")
	from, to, ok2 := strings.Cut(span, "..")
	if !ok || !ok2 || dir == "" || test == "" {
		return fail(fmt.Errorf("-blame DIR:TEST GOOD..BAD, got %q %q", target, span))
	}
	root, err := repositoryRoot()
	if err != nil {
		return fail(err)
	}
	store, err := covfile.Open()
	if err != nil {
		return fail(err)
	}
	indexed, err := store.Commits()
	if err != nil {
		return fail(err)
	}
	if len(indexed) == 0 {
		return fail(fmt.Errorf("no coverage store at %s: run go run ./scripts/mutants -measure", store.Dir))
	}
	packages, _, err := testPackages(root)
	if err != nil {
		return fail(err)
	}
	i := slices.IndexFunc(packages, func(p testPackage) bool { return p.dir == dir })
	if i < 0 {
		return fail(fmt.Errorf("%s is no test package of the checkout", dir))
	}
	list, err := gitText(root, "rev-list", "--first-parent", "--reverse", from+".."+to)
	if err != nil {
		return fail(err)
	}
	blob := func(commit string) func(string) ([]byte, bool) {
		return func(p string) ([]byte, bool) {
			data, err := exec.Command("git", "--no-optional-locks", "-C", root, "cat-file", "blob", commit+":"+p).Output()
			return data, err == nil
		}
	}
	// The record: the indexed commit nearest to the range's start.
	record := nearestIndexed(root, indexed, from)
	if record == "" {
		return fail(fmt.Errorf("the coverage store indexes no commit related to %s", from))
	}
	entries, err := store.At(record)
	if err != nil {
		return fail(err)
	}
	entry, ok := entryOf(entries, dir)
	if !ok {
		return fail(fmt.Errorf("the coverage store has no segment of %s at %s", dir, short(record)))
	}
	seg, ok := store.Load(entry.Key)
	if !ok {
		return fail(fmt.Errorf("the index names a segment the store does not hold"))
	}
	var tips []blameTip
	for _, tip := range strings.Fields(list) {
		diff, err := gitText(root, "diff", "--name-only", "-z", "--no-renames", tip+"^1", tip, "--")
		if err != nil {
			return fail(err)
		}
		paths := slices.DeleteFunc(strings.Split(diff, "\x00"), func(s string) bool { return s == "" })
		tips = append(tips, blameTip{ID: tip, Changes: tipChanges(paths, blob(record), blob(tip+"^1"), blob(tip))})
	}
	names := []string{test}
	pick := func(changes []fileChange) roundPick {
		return pickRound(entry, seg, packages[i].closure, changes, names)
	}
	hits := blame(tips, test, pick)
	fmt.Fprintf(out, "blame %s over %d tips, record %s: %d tips reach it\n", target, len(tips), short(record), len(hits))
	for _, h := range hits {
		subject, _ := gitText(root, "log", "-1", "--format=%s", h.ID)
		fmt.Fprintf(out, "%s\t%s\t%s\n", short(h.ID), h.Reason, subject)
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "gate: no tip of the range reaches the test by the record: the red is not the range's (flaky, or outside the record)")
	}
	return 0
}
