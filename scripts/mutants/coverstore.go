package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
	"ergo.local/prototype/scripts/mutants/covfile"
)

// The coverage store (GATE2 GT6). The coverage pass runs every test of
// every suite once with coverage, which takes hours for the checker's and
// the lowering's suites, and it ran again in every lane for every new
// tree. A suite's coverage (which blocks of the covered packages each of
// its tests runs) depends only on what its tests read: the suite's closure
// with the closures of the tools it runs (the inputs rowKey reads), the
// covered packages, the runner and the toolchain. So each suite's segment
// of the coverage is stored under that key, in the store beside the
// verdicts ($ERGO_MUTANTS_CACHE), and a pass measures only the suites whose
// key changed: a lane on main's head, or a few commits past it, takes
// every segment its edits did not reach. Whoever measures first fills the
// store; `-measure` on main's head after each landing leaves every lane's
// segments. A segment with a test that failed on the unmutated tree is not
// stored (it kills nothing, and a flaky failure must not stick). -fresh
// measures every suite and stores nothing (the reference).

// coverVersion names the coverage store's format and key.
const coverVersion = covfile.Version

// segmentKey is the key of a suite's coverage: the suite's identity, the
// covered packages, the toolchain, and the content of every file the
// suite's tests read (closure; nil: every tracked and untracked file).
func segmentKey(in *reuseInputs, s Suite, cover []string, closure *replay.Closure) string {
	h := sha256.New()
	put := func(parts ...string) {
		for _, p := range parts {
			fmt.Fprintf(h, "%d:%s", len(p), p)
		}
		h.Write([]byte{'\n'})
	}
	put("version", coverVersion)
	put("toolchain", in.toolchain)
	put("mode", covfile.Mode)
	put("suite", s.Name, s.Package, s.Module, s.Tags)
	put(append([]string{"command"}, s.Command...)...)
	put(append([]string{"env"}, s.Env...)...)
	put(append([]string{"only", fmt.Sprint(s.Only != nil)}, s.Only...)...)
	put(append([]string{"named"}, s.Named...)...)
	put(append([]string{"cover"}, cover...)...)
	for _, p := range slices.Sorted(maps.Keys(in.files)) {
		if closure == nil || closure.Covers(p) {
			put("file", p, in.files[p])
		}
	}
	if closure != nil {
		for _, p := range in.ignores {
			if closure.Covers(p) {
				if digest, ok := in.ignored(p); ok {
					put("ignored", p, digest)
				}
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// segment is the part of c that the tests of suite run, with the builds
// of the suite and of its tools.
func segment(c *coverage, suite string, tools []string) *coverage {
	s := &coverage{Files: map[string][]covBlock{}, Builds: map[string]float64{}}
	local := map[int]int{}
	for i, t := range c.Tests {
		if name, _ := splitID(t.ID); name == suite {
			local[i] = len(s.Tests)
			copied := *t
			s.Tests = append(s.Tests, &copied)
		}
	}
	for file, blocks := range c.Files {
		for _, b := range blocks {
			var tests []int
			for _, i := range b.Tests {
				if j, ok := local[i]; ok {
					tests = append(tests, j)
				}
			}
			if len(tests) > 0 {
				s.Files[file] = append(s.Files[file], covBlock{Start: b.Start, End: b.End, Tests: tests})
			}
		}
	}
	for _, name := range append([]string{suite}, tools...) {
		if seconds, ok := c.Builds[name]; ok {
			s.Builds[name] = seconds
		}
	}
	return s
}

// mergeCoverage joins segments into one coverage of the tree key.
func mergeCoverage(key string, segments []*coverage) *coverage {
	c := &coverage{Key: key, Files: map[string][]covBlock{}, Builds: map[string]float64{}}
	for _, s := range segments {
		offset := len(c.Tests)
		c.Tests = append(c.Tests, s.Tests...)
		for file, blocks := range s.Files {
			for _, b := range blocks {
				tests := make([]int, len(b.Tests))
				for k, i := range b.Tests {
					tests[k] = i + offset
				}
				c.Files[file] = append(c.Files[file], covBlock{Start: b.Start, End: b.End, Tests: tests})
			}
		}
		for name, seconds := range s.Builds {
			c.Builds[name] = max(c.Builds[name], seconds)
		}
	}
	for _, blocks := range c.Files {
		slices.SortStableFunc(blocks, func(a, b covBlock) int {
			if a.Start != b.Start {
				return a.Start - b.Start
			}
			return a.End - b.End
		})
	}
	c.Reindex()
	return c
}

// coverStore holds coverage segments by key (covfile.Store).
type coverStore = covfile.Store

// openCoverStore is the store beside the verdicts' store (reuse.go).
func openCoverStore() (coverStore, error) { return covfile.Open() }

// assembleCoverage is the coverage of suites under the tree key: each
// suite's stored segment at its key (keys), and for the rest one measure
// of those suites alone, split into segments. A measured segment is stored
// when no test of it failed and the suite's key after the measure (ends)
// is still its key. It returns the coverage and the suites it measured.
func assembleCoverage(key string, suites []Suite, keys map[string]string, load func(string) (*coverage, bool), measure func([]Suite) (*coverage, error), ends func() (map[string]string, error), save func(string, *coverage) error) (*coverage, []string, error) {
	segments := make([]*coverage, len(suites))
	var missing []Suite
	var measured []string
	for i, s := range suites {
		if c, ok := load(keys[s.Name]); ok {
			segments[i] = c
			continue
		}
		missing = append(missing, s)
		measured = append(measured, s.Name)
	}
	if len(missing) > 0 {
		c, err := measure(missing)
		if err != nil {
			return nil, nil, err
		}
		end, endErr := ends()
		for i, s := range suites {
			if segments[i] != nil {
				continue
			}
			segments[i] = segment(c, s.Name, toolsOf(&s))
			failed := slices.ContainsFunc(segments[i].Tests, func(t *covTest) bool { return t.Failed })
			if endErr == nil && !failed && end[s.Name] == keys[s.Name] {
				_ = save(keys[s.Name], segments[i])
			}
		}
	}
	return mergeCoverage(key, segments), measured, nil
}

// coverageOfTree measures the coverage of the engine's suites, taking each
// suite's segment from the store (Config.CoverStore) where its key holds.
func (e *engine) coverageOfTree(ctx context.Context) (*coverage, error) {
	// A command runs only where a row names it (Suite.Commands, judge.go;
	// gtb-command-measured).
	suites := slices.DeleteFunc(slices.Clone(e.cfg.Suites), func(s Suite) bool { return s.Commands != nil })
	if e.cfg.CoverStore == "" {
		e.logf("coverage: measuring every suite (tree %s)", e.key)
		return e.measure(ctx, suites)
	}
	start := cleanCommit(e.cfg.Root)
	keys, err := e.suiteKeys()
	if err != nil {
		e.logf("coverage: no segment reused: %v", err)
		return e.measure(ctx, suites)
	}
	store := coverStore{Dir: e.cfg.CoverStore}
	measure := func(suites []Suite) (*coverage, error) {
		var names []string
		for _, s := range suites {
			names = append(names, s.Name)
		}
		e.logf("coverage: measuring %s (tree %s); the other suites' segments are stored at %s", strings.Join(names, ", "), e.key, store.Dir)
		return e.measure(ctx, suites)
	}
	c, measured, err := assembleCoverage(e.key, suites, keys, store.Load, measure, e.suiteKeys, store.Save)
	if err == nil {
		e.logf("coverage: %d of %d suites' segments reused from %s", len(suites)-len(measured), len(suites), store.Dir)
		if start != "" && cleanCommit(e.cfg.Root) == start {
			n, err := indexSegments(store, start, e.cfg.Root, suites, keys, e.cfg.Cover)
			if err != nil {
				e.logf("coverage: index: %v", err)
			}
			e.logf("coverage: %d segments indexed at %s", n, start)
		}
	}
	return c, err
}

// cleanCommit is HEAD of the checkout at root when no tracked file is
// changed and no untracked file is there, else "": a reader diffs the
// commit of an index entry against its change, so the segment must be
// the coverage of that commit's tree.
func cleanCommit(root string) string {
	status := exec.Command("git", "--no-optional-locks", "status", "--porcelain", "--untracked-files=normal")
	status.Dir, status.Env = root, goEnviron()
	out, err := status.Output()
	if err != nil || len(out) > 0 {
		return ""
	}
	head := exec.Command("git", "--no-optional-locks", "rev-parse", "HEAD")
	head.Dir, head.Env = root, goEnviron()
	out, err = head.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// indexSegments records, at commit, each suite whose segment the store
// holds at its key (reused or measured and stored): the index a reader
// finds a commit's segments by (covfile.Entry). It returns how many.
func indexSegments(store coverStore, commit, root string, suites []Suite, keys map[string]string, cover []string) (int, error) {
	goEnv, err := covfile.GoEnv(goCommand(), root, goEnviron())
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, s := range suites {
		// A suite of named tests (Only) is no record of its package: a
		// reader would take it for one, and it would replace the whole
		// suite's entry of the same name at the commit.
		if s.Only != nil {
			continue
		}
		if _, ok := store.Load(keys[s.Name]); !ok {
			continue
		}
		e := covfile.Entry{Suite: s.Name, Package: s.Package, Module: s.Module, Tags: s.Tags, Cover: cover, Key: keys[s.Name], Mode: covfile.Mode, Commit: commit, GoEnv: goEnv, Recorded: time.Now().UTC()}
		if err := store.Record(e); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// suiteKeys are the segment keys of the engine's suites over the checkout
// now.
func (e *engine) suiteKeys() (map[string]string, error) {
	in, err := gatherReuse(e.cfg.Root, nil)
	if err != nil {
		return nil, err
	}
	listed, err := listModule(e.cfg.Root)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, s := range e.cfg.Suites {
		keys[s.Name] = segmentKey(in, s, e.cfg.Cover, suiteClosure(listed, s, e.cfg.Tools))
	}
	return keys, nil
}

// suiteClosure is what the tests of s read: its test package's closure
// with the closures of the tools it runs, or nil (every file) for a
// command suite, a tagged build or a nested module.
func suiteClosure(listed *listedModule, s Suite, tools []Tool) *replay.Closure {
	if s.Command != nil || s.Module != "" || s.Tags != "" || s.Package == "" {
		return nil
	}
	c := listed.testClosure(strings.TrimPrefix(s.Package, "./"))
	for _, t := range tools {
		if slices.Contains(toolsOf(&s), t.Name) {
			c = unionClosure(c, listed.programClosure(strings.TrimPrefix(t.Package, "./")))
		}
	}
	return c
}
