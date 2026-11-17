package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The coverage pass measures only what can cover the selected mutants
// (MQ-tell: `-run ^mq- lower runtime/v1` passed its 90 minute bound in the
// coverage pass, measuring every test of check, lower, std, api, cmd/ergo,
// every example and runtime/v1, before it judged one mutant). A test's
// coverage is the blocks its own process ran: its test binary's, or a tool's
// the suite runs (the ergo CLI, the examples runner), each built with
// -coverpkg. A binary that does not compile a package records no block of
// it. So a suite whose test binary, and every tool it runs, compiles none of
// the packages of the selected mutants' Go files can cover none of their
// lines: its tests are not measured, and its binary is not built. A file
// that is not Go counts as the package that embeds it (the nearest
// directory at or above it that holds Go files), since only a binary that
// compiles that package reads it as embedded data or runs its cover marks
// (ergolines.go); a file no package embeds (an example's .ergo) has no
// package. A test a row names (its killers, its anchor's tests) judges its
// mutant whatever it compiles, so its suite is measured too: the coverage
// store keeps whole suites (coverstore.go).

// coverScope is what the coverage pass measures: the suites whose tests it
// runs whole, and the tests the rows name.
type coverScope struct {
	suites  map[string]bool
	anchors map[string]bool
	// skipped are the suites it does not measure, for the receipt.
	skipped []string
}

// key is the scope's part of the coverage file's name: a coverage of one
// scope is never read for another.
func (s coverScope) key() string {
	var words []string
	for name := range s.suites {
		words = append(words, "suite "+name)
	}
	for id := range s.anchors {
		words = append(words, "anchor "+id)
	}
	slices.Sort(words)
	sum := sha256.Sum256([]byte(strings.Join(words, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// of is the suites the pass measures, in their order.
func (s coverScope) of(suites []Suite) []Suite {
	var out []Suite
	for _, su := range suites {
		if s.suites[su.Name] {
			out = append(out, su)
		}
	}
	return out
}

// targets are the import paths of the packages of the selected mutants'
// files (the file, or the anchor's file when the anchor names lines; for a
// file that is not Go the package that embeds it, embedder), and the tests
// the rows name.
func targets(root, module string, jobs []Job) ([]string, map[string]bool) {
	var pkgs []string
	anchors := map[string]bool{}
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			if j.Select != nil && !j.Select.MatchString(m.Name) {
				continue
			}
			for _, id := range killerIDs(m) {
				anchors[id] = true
			}
			file := m.File
			if m.Anchor != nil && m.Anchor.Text != "" && m.Anchor.File != "" {
				file = m.Anchor.File
			}
			dir := path.Dir(file)
			if !strings.HasSuffix(file, ".go") {
				var ok bool
				if dir, ok = embedder(root, dir); !ok {
					continue // no package reads it: only the tests it names cover it
				}
			}
			pkg := module
			if dir != "." {
				pkg += "/" + dir
			}
			if !slices.Contains(pkgs, pkg) {
				pkgs = append(pkgs, pkg)
			}
		}
	}
	slices.Sort(pkgs)
	return pkgs, anchors
}

// embedder is the directory, relative to root, of the Go package that can
// embed a file of dir: the nearest directory at or above dir that holds a
// non-test Go file. False when none does below the module root.
func embedder(root, dir string) (string, bool) {
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				return d, true
			}
		}
	}
	return "", false
}

// planScope chooses the suites to measure: those whose test binary
// (suiteDeps, by suite name) or one of whose tools (toolDeps, by tool
// name) compiles a package of pkgs.
func planScope(suites []Suite, suiteDeps, toolDeps map[string][]string, pkgs []string, anchors map[string]bool) coverScope {
	s := coverScope{suites: map[string]bool{}, anchors: anchors}
	reaches := func(deps []string) bool {
		return slices.ContainsFunc(deps, func(d string) bool { return slices.Contains(pkgs, d) })
	}
	for i := range suites {
		su := &suites[i]
		if su.Commands != nil {
			// A command runs only where a row names it (phase A, judge.go).
			s.skipped = append(s.skipped, su.Name)
			continue
		}
		ok := reaches(suiteDeps[su.Name])
		for _, tool := range toolsOf(su) {
			ok = ok || reaches(toolDeps[tool])
		}
		for id := range anchors {
			if name, _ := splitID(id); name == su.Name {
				ok = true
			}
		}
		if ok {
			s.suites[su.Name] = true
		} else {
			s.skipped = append(s.skipped, su.Name)
		}
	}
	return s
}

// scope lists what each suite's test binary and each tool compiles (go
// list -deps) and plans the pass for the selected mutants of jobs.
func (e *engine) scope(jobs []Job) (coverScope, error) {
	pkgs, anchors := targets(e.cfg.Root, e.module, jobs)
	suiteDeps, toolDeps := map[string][]string{}, map[string][]string{}
	for _, s := range e.cfg.Suites {
		if s.Package == "" {
			continue
		}
		deps, err := e.deps(true, s.Package)
		if err != nil {
			return coverScope{}, err
		}
		suiteDeps[s.Name] = deps
	}
	for _, t := range e.cfg.Tools {
		deps, err := e.deps(false, t.Package)
		if err != nil {
			return coverScope{}, err
		}
		toolDeps[t.Name] = deps
	}
	return planScope(e.cfg.Suites, suiteDeps, toolDeps, pkgs, anchors), nil
}

// deps is what pkg compiles (with test, its test binary), as import paths
// without the test variant's suffix.
func (e *engine) deps(test bool, pkg string) ([]string, error) {
	args := []string{"list", "-deps", "-f", "{{.ImportPath}}"}
	if test {
		args = append(args, "-test")
	}
	cmd := exec.Command(goCommand(), append(args, pkg)...)
	cmd.Dir, cmd.Env = e.cfg.Root, e.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps %s: %w", pkg, err)
	}
	var deps []string
	for _, line := range strings.Fields(strings.ReplaceAll(string(out), " [", "\x00[")) {
		if p, _, _ := strings.Cut(line, "\x00"); !slices.Contains(deps, p) {
			deps = append(deps, p)
		}
	}
	return deps, nil
}
