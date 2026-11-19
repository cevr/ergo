package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
)

// sweepKeySeedVariable replays one seed of TestSweepReplayAgreesWithAFullSweep.
const sweepKeySeedVariable = "ERGO_SWEEP_KEY_SEED"

// The incremental sweep (keySweeps) gives the full sweep's verdicts: a
// seeded property over a model checkout, a model sweep and a model gate
// store. The model sweep of a package reads its own files and, through
// "read:<path>" words in what it reads, any path; it passes when every
// read lies in the package's closure (replay.Closure.Covers) and is not
// denied by the sweeper's rule file, every declared input is read, and
// every directory the rule file names is a test package. After each seeded
// edit (a file's words, a test file added or removed, the rule file, a file
// outside every closure) the gate's way (a recorded pass replays while the
// step's package key is unchanged; a failure records nothing) gives every
// package the verdict that sweeping all of them gives. An edit outside
// every closure reruns no sweep. A failing seed prints its edits and the
// replay command.
//
// Mutants (testdata/mutants.json): gt1-sweep-unkeyed (no Package, no
// replay), gt1-sweeper-dropped (the rule file's edit replays a stale pass),
// gt1-declared-dropped, gt1-table-names-dropped, gt1-table-names-unfiltered,
// gt1-literals-none.
func TestSweepReplayAgreesWithAFullSweep(t *testing.T) {
	seeds, edits := 300, 40
	if testing.Short() {
		seeds = 60
	}
	first := 0
	if v := os.Getenv(sweepKeySeedVariable); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s=%q: want a seed number", sweepKeySeedVariable, v)
		}
		first, seeds = n, 1
	}
	replayed := 0
	for seed := first; seed < first+seeds; seed++ {
		replayed += checkSweepSeed(t, seed, edits)
		if t.Failed() {
			return
		}
	}
	if replayed == 0 {
		t.Errorf("no sweep replayed in %d seeds: the property checks no replay", seeds)
	}
}

// sweepModel is a model checkout: files by path, the closure of each
// package directory, and the sweeper's rule.
type sweepModel struct {
	files    map[string]string
	closures map[string]*replay.Closure
	packages []string
	data     []string
	names    []string
	deny     []string
}

const modelRule = "internal/closure/rule.go"

func newSweepModel(r *rand.Rand) *sweepModel {
	m := &sweepModel{files: map[string]string{}, closures: map[string]*replay.Closure{}}
	for i := range 5 {
		m.packages = append(m.packages, fmt.Sprintf("pkg/p%d", i))
	}
	for i := range 4 {
		m.data = append(m.data, fmt.Sprintf("data/f%d", i))
	}
	m.files["other/noise"] = "0"
	for _, d := range m.data {
		m.files[d] = ""
	}
	for _, p := range m.packages {
		m.files[p+"/a.go"] = ""
		m.files[p+"/testdata/t"] = ""
		if r.IntN(5) > 0 {
			m.files[p+"/a_test.go"] = ""
		}
		c := &replay.Closure{Dir: p, Deps: []string{p}}
		for _, q := range m.packages {
			if q != p && r.IntN(3) == 0 {
				c.Deps = append(c.Deps, q)
			}
		}
		for _, d := range m.data {
			if r.IntN(4) == 0 {
				c.Declared = append(c.Declared, replay.Allowance{Path: d, Grant: replay.GrantFile})
			}
		}
		if r.IntN(3) == 0 {
			q := m.packages[r.IntN(len(m.packages))]
			c.Declared = append(c.Declared, replay.Allowance{Path: q, Grant: replay.GrantProgram})
			c.Programs = map[string]*replay.Closure{q: {Dir: q, Deps: []string{q}}}
		}
		m.closures[p] = c
	}
	m.files[sweeperDir+"/a_test.go"] = ""
	m.closures[sweeperDir] = &replay.Closure{Dir: sweeperDir, Deps: []string{sweeperDir}}
	m.writeRule()
	for _, p := range slices.Sorted(maps.Keys(m.files)) {
		if p != modelRule && r.IntN(2) == 0 {
			m.files[p] = m.words(r)
		}
	}
	return m
}

// words is a random file content: up to two reads of any path.
func (m *sweepModel) words(r *rand.Rand) string {
	paths := slices.Sorted(maps.Keys(m.files))
	var words []string
	for range r.IntN(3) {
		if r.IntN(3) == 0 {
			words = append(words, "read:"+paths[r.IntN(len(paths))])
		}
	}
	return strings.Join(words, " ") + " " + strconv.Itoa(r.IntN(1000))
}

func (m *sweepModel) writeRule() {
	m.files[modelRule] = fmt.Sprintf("package closure\n\nvar names = %#v\n\nvar deny = %#v\n", m.names, m.deny)
}

// tested are the model's test packages: the directories with a test file.
func (m *sweepModel) tested() []testPackage {
	var tested []testPackage
	for _, dir := range append(slices.Clone(m.packages), sweeperDir) {
		if _, ok := m.files[dir+"/a_test.go"]; ok {
			tested = append(tested, testPackage{importPath: "m/" + dir, dir: dir, closure: m.closures[dir]})
		}
	}
	return tested
}

// facts are the model checkout as the gate keys it.
func (m *sweepModel) facts() *replay.Facts {
	facts := &replay.Facts{Executable: "gate", Root: "/model"}
	dirs := map[string]bool{}
	for p, content := range m.files {
		sum := sha256.Sum256([]byte(content))
		facts.Entries = append(facts.Entries, replay.Entry{Path: p, Class: replay.Tracked, Kind: replay.File, Value: hex.EncodeToString(sum[:])})
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		facts.Entries = append(facts.Entries, replay.Entry{Path: d, Kind: replay.Directory})
	}
	return facts
}

// sweep is the model sweep's verdict for the package in dir.
func (m *sweepModel) sweep(dir string) bool {
	c := m.closures[dir]
	read := map[string]bool{}
	var queue []string
	for p := range m.files {
		if strings.HasPrefix(p, dir+"/") {
			queue = append(queue, p)
		}
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if read[p] {
			continue
		}
		read[p] = true
		for _, word := range strings.Fields(m.files[p]) {
			if target, ok := strings.CutPrefix(word, "read:"); ok {
				queue = append(queue, target)
			}
		}
	}
	for p := range read {
		if !c.Covers(p) || slices.Contains(m.deny, p) {
			return false
		}
	}
	for _, a := range c.Declared {
		if a.Grant == replay.GrantFile && !read[a.Path] {
			return false
		}
	}
	for _, name := range m.names {
		if _, ok := m.files[name+"/a_test.go"]; !ok {
			return false
		}
	}
	return true
}

// edit makes one seeded edit and says what it was; noise is true for an
// edit outside every closure.
func (m *sweepModel) edit(r *rand.Rand) (what string, noise bool) {
	switch r.IntN(5) {
	case 0:
		p := m.packages[r.IntN(len(m.packages))] + "/a_test.go"
		if _, ok := m.files[p]; ok {
			delete(m.files, p)
			return "remove " + p, false
		}
		m.files[p] = ""
		return "add " + p, false
	case 1:
		m.names, m.deny = nil, nil
		for _, p := range m.packages {
			if r.IntN(4) == 0 {
				m.names = append(m.names, p)
			}
		}
		paths := slices.Sorted(maps.Keys(m.files))
		for range r.IntN(2) {
			m.deny = append(m.deny, paths[r.IntN(len(paths))])
		}
		m.writeRule()
		return fmt.Sprintf("rule names %v deny %v", m.names, m.deny), false
	case 2:
		m.files["other/noise"] += "+"
		return "noise", true
	default:
		paths := slices.Sorted(maps.Keys(m.files))
		p := paths[r.IntN(len(paths))]
		if p == modelRule {
			p = "other/noise"
		}
		m.files[p] = m.words(r)
		return fmt.Sprintf("write %s %q", p, m.files[p]), p == "other/noise"
	}
}

// checkSweepSeed runs one seed's edits and returns how many sweeps
// replayed.
func checkSweepSeed(t *testing.T, seed, edits int) int {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(seed), 7))
	m := newSweepModel(r)
	store := map[string]bool{}
	replayed := 0
	history := []string{"start"}
	for i := 0; i <= edits; i++ {
		noise := false
		if i > 0 {
			var what string
			what, noise = m.edit(r)
			history = append(history, what)
		}
		tested := m.tested()
		var steps []*Step
		for _, p := range tested {
			steps = append(steps, sweepStep(p.dir, 0))
		}
		keySweeps(steps, tested, "", stringLiterals([]byte(m.files[modelRule])))
		facts := m.facts()
		for _, step := range steps {
			full := m.sweep(step.Sweep)
			incremental, ran := false, true
			key, problem := "", "no package key"
			if step.Package != nil {
				key, problem = replay.PackageKey(facts, step.Package, packageIdentity(step)...)
			}
			if problem == "" && store[key] {
				incremental, ran = true, false
				replayed++
			} else {
				incremental = m.sweep(step.Sweep)
				if problem == "" {
					if incremental {
						store[key] = true
					} else {
						delete(store, key)
					}
				}
			}
			if incremental != full {
				t.Errorf("seed %d, edit %d: sweep of %s: incremental %v, full %v\nedits:\n  %s\nreplay: %s=%d go test -run TestSweepReplayAgreesWithAFullSweep ./scripts/gate", seed, i, step.Sweep, incremental, full, strings.Join(history, "\n  "), sweepKeySeedVariable, seed)
				return replayed
			}
			if noise && ran && full {
				t.Errorf("seed %d, edit %d: an edit outside every closure reran the passing sweep of %s\nedits:\n  %s\nreplay: %s=%d go test -run TestSweepReplayAgreesWithAFullSweep ./scripts/gate", seed, i, step.Sweep, strings.Join(history, "\n  "), sweepKeySeedVariable, seed)
				return replayed
			}
		}
	}
	return replayed
}
