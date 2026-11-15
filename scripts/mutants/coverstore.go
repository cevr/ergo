package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/scripts/gate/replay"
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
const coverVersion = "ergo-mutants-coverage-v1"

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
	c.reindex()
	return c
}

// coverStore holds coverage segments by key, one gzip JSON file each.
type coverStore struct{ dir string }

// openCoverStore is the store beside the verdicts' store (reuse.go).
func openCoverStore() (coverStore, error) {
	verdicts, err := openReuseStore()
	if err != nil {
		return coverStore{}, err
	}
	return coverStore{filepath.Join(filepath.Dir(verdicts.dir), coverVersion)}, nil
}

func (s coverStore) path(key string) string {
	return filepath.Join(s.dir, key[:2], key+".json.gz")
}

func (s coverStore) load(key string) (*coverage, bool) {
	c, err := loadCoverage(s.path(key))
	return c, err == nil
}

func (s coverStore) save(key string, c *coverage) error {
	return saveCoverage(s.path(key), c)
}

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
	if e.cfg.CoverStore == "" {
		e.logf("coverage: measuring every suite (tree %s)", e.key)
		return e.measure(ctx, e.cfg.Suites)
	}
	keys, err := e.suiteKeys()
	if err != nil {
		e.logf("coverage: no segment reused: %v", err)
		return e.measure(ctx, e.cfg.Suites)
	}
	store := coverStore{e.cfg.CoverStore}
	measure := func(suites []Suite) (*coverage, error) {
		var names []string
		for _, s := range suites {
			names = append(names, s.Name)
		}
		e.logf("coverage: measuring %s (tree %s); the other suites' segments are stored at %s", strings.Join(names, ", "), e.key, store.dir)
		return e.measure(ctx, suites)
	}
	c, measured, err := assembleCoverage(e.key, e.cfg.Suites, keys, store.load, measure, e.suiteKeys, store.save)
	if err == nil {
		e.logf("coverage: %d of %d suites' segments reused from %s", len(e.cfg.Suites)-len(measured), len(e.cfg.Suites), store.dir)
	}
	return c, err
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
