package main

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// coverSeedVariable replays one seed of TestStoredCoverageAgreesWithAFreshMeasure.
const coverSeedVariable = "ERGO_MUTANTS_COVER_SEED"

// A coverage assembled from stored segments (assembleCoverage, segment,
// mergeCoverage, segmentKey, coverStore) is the coverage a fresh measure of
// every suite gives: a seeded property over reuse_test.go's model checkout
// and a model measure. A test of a suite runs blocks of each file the suite
// reads (its closure, ignored files included; the examples suite every
// tracked file), at lines drawn from the file's content and the test; a
// test fails on the unmutated tree when it reads "guard:r0:b"; each suite's
// build and its tool's build take a time of their own. After each seeded
// edit the assembling pass, with the real store in a temporary directory,
// must equal a fresh measure; a segment with a failed test is never stored;
// a measure during which a file changed (and changed back after) stores
// nothing; and after an edit outside every Go suite's closure only the
// examples suite is measured. A failing seed prints its edits and the
// replay command.
//
// Mutants (testdata/mutants.json): gt6-key-closure-dropped,
// gt6-key-ignored-dropped, gt6-store-failed, gt6-ends-unchecked,
// gt6-merge-offset, gt6-segment-tools, gt6-no-reuse.
func TestStoredCoverageAgreesWithAFreshMeasure(t *testing.T) {
	seeds, edits := 100, 30
	if testing.Short() {
		seeds = 20
	}
	first := 0
	if v := os.Getenv(coverSeedVariable); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s=%q: want a seed number", coverSeedVariable, v)
		}
		first, seeds = n, 1
	}
	reused := 0
	for seed := first; seed < first+seeds; seed++ {
		reused += checkCoverSeed(t, seed, edits)
		if t.Failed() {
			return
		}
	}
	if reused == 0 {
		t.Errorf("no segment reused in %d seeds: the property checks no reuse", seeds)
	}
}

// modelSuites are the model's suites: one per Go package (the first runs
// the tool t0) and the examples suite.
func modelSuites(m *reuseModel) []Suite {
	var out []Suite
	for i, name := range m.suites {
		s := Suite{Name: name, Package: "./" + name}
		if name == "examples" {
			s = Suite{Name: name, Command: []string{"{bin:examples}", "{test}"}}
		} else if i == 0 {
			s.Env = []string{"T={bin:t0}"}
		}
		out = append(out, s)
	}
	return out
}

func modelTests(s Suite) []string {
	if s.Name == "examples" {
		return []string{"e0", "e1"}
	}
	return []string{"TestA", "TestB"}
}

// measure is the model's coverage of suites over the model checkout now.
func (m *reuseModel) measure(suites []Suite) *coverage {
	c := &coverage{Key: "tree", Builds: map[string]float64{}}
	k := &collector{blocks: map[blockKey][]int{}}
	for _, s := range suites {
		c.Builds[s.Name] = float64(len(s.Name))
		for _, tool := range toolsOf(&s) {
			c.Builds[tool] = 7
		}
		closure := m.closures[s.Name]
		read := map[string]string{}
		for p, content := range m.files {
			if closure == nil || closure.Covers(p) {
				read[p] = content
			}
		}
		if closure != nil {
			for p, content := range m.ignored {
				if closure.Covers(p) {
					read[p] = content
				}
			}
		}
		for _, name := range modelTests(s) {
			i := len(c.Tests)
			test := &covTest{ID: s.Name + ":" + name, Seconds: 1}
			c.Tests = append(c.Tests, test)
			for _, p := range slices.Sorted(maps.Keys(read)) {
				if slices.Contains(strings.Fields(read[p]), "guard:r0:b") && name != "e1" {
					test.Failed, test.Detail = true, "fail: reads guard:r0:b"
				}
				d := digest(p + "\x00" + read[p] + "\x00" + test.ID)
				for b := 0; b < 3; b++ {
					if d[2*b]%2 == 0 {
						start := 1 + int(d[2*b+1])%20
						key := blockKey{file: p, start: start, end: start + int(d[2*b])%3}
						if !slices.Contains(k.blocks[key], i) {
							k.blocks[key] = append(k.blocks[key], i)
						}
					}
				}
			}
		}
	}
	c.Files = k.files()
	c.Reindex()
	return c
}

// keys are the segment keys of suites over the model checkout now.
func (m *reuseModel) keys(suites []Suite) map[string]string {
	in := m.inputs()
	out := map[string]string{}
	for _, s := range suites {
		out[s.Name] = segmentKey(in, s, []string{"./pkg/..."}, m.closures[s.Name])
	}
	return out
}

// coverText is c in a canonical text: each test with its failure, the
// blocks it runs and the builds.
func coverText(c *coverage) string {
	runs := map[string][]string{}
	for file, blocks := range c.Files {
		for _, b := range blocks {
			for _, i := range b.Tests {
				id := c.Tests[i].ID
				runs[id] = append(runs[id], fmt.Sprintf("%s:%d-%d", file, b.Start, b.End))
			}
		}
	}
	var lines []string
	for _, test := range c.Tests {
		r := runs[test.ID]
		slices.Sort(r)
		lines = append(lines, fmt.Sprintf("%s failed=%v %s", test.ID, test.Failed, strings.Join(r, " ")))
	}
	slices.Sort(lines)
	for _, name := range slices.Sorted(maps.Keys(c.Builds)) {
		lines = append(lines, fmt.Sprintf("build %s %g", name, c.Builds[name]))
	}
	return strings.Join(lines, "\n")
}

// checkCoverSeed runs one seed's edits and returns how many segments were
// reused.
func checkCoverSeed(t *testing.T, seed, edits int) int {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(seed), 13))
	m := newReuseModel(r)
	suites := modelSuites(m)
	store := coverStore{Dir: t.TempDir()}
	history := []string{"start"}
	fail := func(i int, format string, args ...any) {
		t.Errorf("seed %d, edit %d: %s\nedits:\n  %s\nreplay: %s=%d go test -run TestStoredCoverageAgreesWithAFreshMeasure ./scripts/mutants", seed, i, fmt.Sprintf(format, args...), strings.Join(history, "\n  "), coverSeedVariable, seed)
	}
	total, lastDuring := 0, true
	for i := 0; i <= edits; i++ {
		quiet := false
		if i > 0 {
			var what string
			what, quiet = m.edit(r)
			history = append(history, what)
		}
		keys := m.keys(suites)
		// Now and then a file changes during the measure and changes back
		// after it: what the measure read is no coverage of the keys.
		var during, before string
		if r.IntN(5) == 0 {
			paths := slices.Sorted(maps.Keys(m.files))
			during = paths[r.IntN(len(paths))]
			before = m.files[during]
			history = append(history, fmt.Sprintf("(during the measure: %s changes, then back)", during))
		}
		var saved []string
		save := func(key string, c *coverage) error {
			saved = append(saved, key)
			return store.Save(key, c)
		}
		measure := func(s []Suite) (*coverage, error) {
			if during != "" {
				m.files[during] = before + " changed"
			}
			return m.measure(s), nil
		}
		ends := func() (map[string]string, error) { return m.keys(suites), nil }
		got, measured, err := assembleCoverage("tree", suites, keys, store.Load, measure, ends, save)
		if during != "" {
			m.files[during] = before
		}
		if err != nil {
			fail(i, "assemble: %v", err)
			return total
		}
		total += len(suites) - len(measured)
		for _, s := range suites {
			if !slices.Contains(saved, keys[s.Name]) {
				continue
			}
			if c, ok := store.Load(keys[s.Name]); ok && slices.ContainsFunc(c.Tests, func(t *covTest) bool { return t.Failed }) {
				fail(i, "suite %s: a segment with a failed test was stored", s.Name)
				return total
			}
		}
		if during != "" {
			// What it assembled read the changed file; only the store is
			// checked, by the next edit's assembly.
			lastDuring = true
			continue
		}
		fresh := m.measure(suites)
		if want := coverText(fresh); coverText(got) != want {
			fail(i, "the assembled coverage (measured %v) differs from a fresh measure:\nassembled:\n%s\nfresh:\n%s", measured, coverText(got), want)
			return total
		}
		// After a quiet edit, a Go suite is measured again only when its
		// segment was never stored: a failed test, or a change during the
		// last measure.
		if quiet && !lastDuring {
			for _, name := range measured {
				if name != "examples" && !slices.ContainsFunc(segment(fresh, name, nil).Tests, func(t *covTest) bool { return t.Failed }) {
					fail(i, "an edit outside every Go suite's closure measured %s again (measured %v)", name, measured)
					return total
				}
			}
		}
		lastDuring = false
	}
	return total
}
