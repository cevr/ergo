package main

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
)

// exampleReaders are the test packages whose tests read every program of
// examples/ (their closure table entries, which the closure sweep holds to
// what the tests read): cmd/ergo (the README smoke test, the context-reach
// repro and the package-folder test), internal/sketch/lower (the accepted
// and refusal inventories) and scripts/examples (the laws of every
// example). Batch-10 r2 B1: an edit of the README's opening example broke a cmd/ergo
// test that reads the program by text, and the lane that made it did not
// run cmd/ergo.
var exampleReaders = []string{"cmd/ergo", "internal/sketch/lower", "scripts/examples"}

// TestFastTierReachOfAnExampleEdit: an edit of any file under examples/
// reruns, in the fast tier, every test package whose closure table entry
// covers the file (exampleReaders among them, and the packages that read
// one example, such as internal/sketchfront), and the examples step. Each
// such package has fast steps, and the key of each (its Package closure)
// covers the file, so the edit changes the key and the step cannot replay
// its last pass. The key's own rule, that a changed covered input never
// replays, is TestPackageKeyModel's; this test holds the real table and the
// real fast steps to it.
func TestFastTierReachOfAnExampleEdit(t *testing.T) {
	root := checkoutRoot(t)
	packages, problem, err := testPackages(root)
	if err != nil || problem != "" {
		t.Fatalf("%v %s", err, problem)
	}
	steps, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(root, replay.DeclaredFile))
	if err != nil {
		t.Fatal(err)
	}
	table, err := replay.ParseDeclared(source)
	if err != nil {
		t.Fatal(err)
	}
	// The tests of a package, as the fast tier runs them: its keyed go
	// test steps.
	stepsOf := map[string][]*Step{}
	for _, step := range steps {
		if step.Package == nil {
			continue
		}
		stepsOf[step.Package.Dir] = append(stepsOf[step.Package.Dir], step)
	}
	examplesStep := steps[slices.IndexFunc(steps, func(s *Step) bool { return s.Name == "examples (default)" })]
	var files []string
	err = filepath.WalkDir(filepath.Join(root, "examples"), func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, file)
		files = append(files, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("%d files under examples/; want every program's", len(files))
	}
	// Whether a reader's every test builds Go, decided once per reader:
	// fullTierOnly parses the package's test files, and deciding it per
	// file parsed internal/sketch/lower's tests once for each of the 5070
	// files under examples/ (GATE-PKG-TIMEOUT: the test took 1590 s of the
	// package's 4826 s at c12).
	fullOnly := map[string]bool{}
	isFullOnly := func(dir string) bool {
		only, seen := fullOnly[dir]
		if !seen {
			var err error
			if only, err = fullTierOnly(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
				t.Fatal(err)
			}
			fullOnly[dir] = only
		}
		return only
	}
	reached := map[string]bool{}
	for _, file := range files {
		// A table entry of the whole tree (internal/residue, the guard,
		// scripts/gate) says nothing of examples/: those packages rerun on
		// every edit (residue, guard), or have no fast step (scripts/gate,
		// a host package).
		var readers []string
		for dir, declared := range table {
			if slices.Contains(declared, replay.Allowance{Path: ".", Grant: replay.GrantSubtree}) {
				continue
			}
			// The declared entries alone: a directory the working tree does
			// not hold, so no file is taken for one of its sources. A
			// program entry covers what the go command reads to build the
			// program (its compiled closure), never examples/; with no
			// closure here it would cover every path (batch-19 combined
			// integration: internal/sketch/std's cmd/ergo entry, DN1).
			declared = slices.DeleteFunc(slices.Clone(declared), func(a replay.Allowance) bool { return a.Grant == replay.GrantProgram })
			if (&replay.Closure{Dir: "-", Declared: declared}).Covers(file) {
				readers = append(readers, dir)
			}
		}
		for _, dir := range exampleReaders {
			if !slices.Contains(readers, dir) {
				t.Errorf("%s: the closure table of %s does not cover it", file, dir)
			}
		}
		for _, dir := range readers {
			// A package whose every test builds Go has no fast step
			// (fullTierOnly: internal/selfhostcheck); the full tier runs it.
			if isFullOnly(dir) {
				continue
			}
			at := slices.IndexFunc(packages, func(p testPackage) bool { return p.dir == dir })
			if at < 0 || !packages[at].closure.Covers(file) {
				t.Errorf("%s: %s reads it (closure table) and its fast-tier key does not cover it", file, dir)
				continue
			}
			if !moduleLookup(file) {
				reached[dir] = true
			}
			if len(stepsOf[dir]) == 0 {
				t.Errorf("%s: %s reads it and has no fast-tier test step", file, dir)
			}
			for _, step := range stepsOf[dir] {
				if !step.Package.Covers(file) {
					t.Errorf("%s: %s reads it and the key of %q does not cover it", file, dir, step.Name)
				}
			}
		}
		if !slices.ContainsFunc(examplesStep.Inputs.Installed, func(dir string) bool { return strings.HasPrefix(file, dir+"/") }) {
			t.Errorf("%s: the examples step's installed inputs %v do not cover it", file, examplesStep.Inputs.Installed)
		}
	}
	t.Logf("%d files under examples/; fullTierOnly decided once for each of %d readers", len(files), len(fullOnly))
	for _, dir := range append(slices.Clone(exampleReaders), "internal/sketchfront", "internal/sketch/check") {
		if !reached[dir] {
			t.Errorf("no file under examples/ reaches %s; reached %v", dir, reached)
		}
	}
}

// moduleLookup reports whether the file has a name the go command looks up
// in every directory (replay's moduleLookups): every package's key covers
// it, whatever the package reads.
func moduleLookup(file string) bool {
	return slices.Contains([]string{"go.mod", "go.work", "go.work.sum", "vendor"}, path.Base(file))
}
