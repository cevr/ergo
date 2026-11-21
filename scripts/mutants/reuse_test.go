package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
)

// reuseSeedVariable replays one seed of TestReusedVerdictsAgreeWithAFreshPass.
const reuseSeedVariable = "ERGO_MUTANTS_REUSE_SEED"

// A pass that reuses stored verdicts (partitionReuse, storeVerdicts,
// reuseStore, rowKey) gives every row the verdict a fresh pass gives: a
// seeded property over a model checkout and a model judge. A row is killed
// when one of its killers reads its mutated file and a file that holds
// "guard:<row>:<edit>" (its current To); a Go suite reads its closure
// (replay.Closure.Covers, ignored files included), the examples suite every
// file. After each seeded edit (a file's words, a file added or removed,
// an ignored file, a row's edit or killers, a file outside every closure)
// the reusing pass, with the real store in a temporary directory, must
// give each row the fresh verdict, and after an edit outside every Go
// suite's closure a killed row whose killers are Go suites is reused, not
// judged. A failing seed prints its edits and the replay command.
//
// Mutants (testdata/mutants.json): gt2-edit-unkeyed, gt2-closure-unkeyed,
// gt2-whole-dropped, gt2-ignored-unkeyed, gt2-no-reuse,
// gt2-store-survivor.
func TestReusedVerdictsAgreeWithAFreshPass(t *testing.T) {
	seeds, edits := 200, 30
	if testing.Short() {
		seeds = 40
	}
	first := 0
	if v := os.Getenv(reuseSeedVariable); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s=%q: want a seed number", reuseSeedVariable, v)
		}
		first, seeds = n, 1
	}
	reused := 0
	for seed := first; seed < first+seeds; seed++ {
		reused += checkReuseSeed(t, seed, edits)
		if t.Failed() {
			return
		}
	}
	if reused == 0 {
		t.Errorf("no verdict reused in %d seeds: the property checks no reuse", seeds)
	}
}

type reuseModel struct {
	files, ignored map[string]string
	closures       map[string]*replay.Closure
	suites         []string
	ledger         *Ledger
}

const modelLedger = "pkg/testdata/mutants.json"

func newReuseModel(r *rand.Rand) *reuseModel {
	m := &reuseModel{files: map[string]string{}, ignored: map[string]string{}, closures: map[string]*replay.Closure{}}
	var pkgs []string
	for i := range 4 {
		pkgs = append(pkgs, fmt.Sprintf("pkg/p%d", i))
	}
	m.files["other/noise"] = "0"
	m.files["data/d0"] = ""
	m.files["examples/e0/main.ergo"] = ""
	for _, p := range pkgs {
		m.files[p+"/a.go"] = ""
		m.files[p+"/a_test.go"] = ""
		m.ignored[p+"/testdata/gen"] = ""
		c := &replay.Closure{Dir: p, Deps: []string{p}}
		for _, q := range pkgs {
			if q != p && r.IntN(3) == 0 {
				c.Deps = append(c.Deps, q)
			}
		}
		if r.IntN(3) == 0 {
			c.Declared = append(c.Declared, replay.Allowance{Path: "data/d0", Grant: replay.GrantFile})
		}
		m.closures[p] = c
		m.suites = append(m.suites, p)
	}
	m.suites = append(m.suites, "examples")
	m.ledger = &Ledger{}
	for i := range 5 {
		row := &Mutant{Name: fmt.Sprintf("r%d", i), From: "x", To: "a"}
		if r.IntN(4) == 0 {
			row.File = "examples/e0/main.ergo"
		} else {
			row.File = pkgs[r.IntN(len(pkgs))] + "/a.go"
		}
		m.killers(r, row)
		m.ledger.Mutants = append(m.ledger.Mutants, row)
	}
	for _, p := range slices.Sorted(maps.Keys(m.files)) {
		if r.IntN(2) == 0 {
			m.files[p] = m.words(r)
		}
	}
	return m
}

func (m *reuseModel) killers(r *rand.Rand, row *Mutant) {
	row.Killers = nil
	for range 1 + r.IntN(2) {
		s := m.suites[r.IntN(len(m.suites))]
		id := s + ":TestK"
		if s == "examples" {
			id = "examples:e0"
		}
		if !slices.Contains(row.Killers, id) {
			row.Killers = append(row.Killers, id)
		}
	}
}

// words is a random content: up to two guards of a random row and edit.
func (m *reuseModel) words(r *rand.Rand) string {
	var words []string
	for range r.IntN(3) {
		if r.IntN(2) == 0 {
			words = append(words, fmt.Sprintf("guard:r%d:%s", r.IntN(5), []string{"a", "b"}[r.IntN(2)]))
		}
	}
	return strings.Join(words, " ") + " " + strconv.Itoa(r.IntN(1000))
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (m *reuseModel) inputs() *reuseInputs {
	in := &reuseInputs{toolchain: "model", files: map[string]string{}, closures: m.closures}
	for p, c := range m.files {
		in.files[p] = digest(c)
	}
	in.ignores = slices.Sorted(maps.Keys(m.ignored))
	in.ignored = func(p string) (string, bool) {
		c, ok := m.ignored[p]
		return digest(c), ok
	}
	return in
}

// judge is the model's fresh verdict on row.
func (m *reuseModel) judge(row *Mutant) Result {
	guard := "guard:" + row.Name + ":" + row.To
	for _, id := range killerIDs(row) {
		suite, _ := splitID(id)
		c := m.closures[suite]
		reads := func(p string) bool { return c == nil || c.Covers(p) }
		if !reads(row.File) {
			continue
		}
		read := []map[string]string{m.files, m.ignored}
		if c == nil {
			// A suite that is no Go package reads sources, not what a
			// build left (rowKey keys no ignored file for it).
			read = read[:1]
		}
		for _, files := range read {
			for p, content := range files {
				if reads(p) && slices.Contains(strings.Fields(content), guard) {
					return Result{Ledger: modelLedger, Name: row.Name, Status: Killed, Killer: id}
				}
			}
		}
	}
	return Result{Ledger: modelLedger, Name: row.Name, Status: Survived}
}

// edit makes one seeded edit; quiet is true for an edit outside every Go
// suite's closure.
func (m *reuseModel) edit(r *rand.Rand) (what string, quiet bool) {
	switch r.IntN(7) {
	case 0:
		row := m.ledger.Mutants[r.IntN(len(m.ledger.Mutants))]
		row.To = []string{"a", "b"}[r.IntN(2)]
		return fmt.Sprintf("row %s edit %s", row.Name, row.To), false
	case 1:
		row := m.ledger.Mutants[r.IntN(len(m.ledger.Mutants))]
		m.killers(r, row)
		return fmt.Sprintf("row %s killers %v", row.Name, row.Killers), false
	case 2:
		p := m.suites[r.IntN(len(m.suites)-1)] + "/b.go"
		if _, ok := m.files[p]; ok {
			delete(m.files, p)
			return "remove " + p, false
		}
		m.files[p] = m.words(r)
		return fmt.Sprintf("add %s %q", p, m.files[p]), false
	case 3:
		p := slices.Sorted(maps.Keys(m.ignored))[r.IntN(len(m.ignored))]
		m.ignored[p] = m.words(r)
		return fmt.Sprintf("ignored %s %q", p, m.ignored[p]), false
	case 4:
		m.files["other/noise"] += "+"
		return "noise", true
	default:
		paths := slices.Sorted(maps.Keys(m.files))
		p := paths[r.IntN(len(paths))]
		m.files[p] = m.words(r)
		return fmt.Sprintf("write %s %q", p, m.files[p]), p == "other/noise"
	}
}

// checkReuseSeed runs one seed's edits and returns how many verdicts were
// reused.
func checkReuseSeed(t *testing.T, seed, edits int) int {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(seed), 11))
	m := newReuseModel(r)
	store := reuseStore{dir: t.TempDir()}
	jobs := []Job{{Path: modelLedger, Ledger: m.ledger}}
	history := []string{"start"}
	fail := func(i int, format string, args ...any) {
		t.Errorf("seed %d, edit %d: %s\nedits:\n  %s\nreplay: %s=%d go test -run TestReusedVerdictsAgreeWithAFreshPass ./scripts/mutants", seed, i, fmt.Sprintf(format, args...), strings.Join(history, "\n  "), reuseSeedVariable, seed)
	}
	total := 0
	for i := 0; i <= edits; i++ {
		quiet := false
		if i > 0 {
			var what string
			what, quiet = m.edit(r)
			history = append(history, what)
		}
		in := m.inputs()
		reused, judge, keys := partitionReuse(jobs, in, store.lookup)
		total += len(reused)
		var judged []Result
		for _, j := range judge {
			for _, row := range j.Ledger.Mutants {
				if j.Select.MatchString(row.Name) {
					judged = append(judged, m.judge(row))
				}
			}
		}
		storeVerdicts(jobs, judged, keys, in, store.record)
		results := inLedgerOrder(jobs, append(reused, judged...))
		if len(results) != len(m.ledger.Mutants) {
			fail(i, "%d results for %d rows", len(results), len(m.ledger.Mutants))
			return total
		}
		for k, row := range m.ledger.Mutants {
			fresh := m.judge(row)
			if got := results[k]; got.Name != row.Name || got.Status != fresh.Status {
				fail(i, "row %s (%s, killers %v): the reusing pass says %s %s by %s (%s), a fresh pass %s %s", row.Name, row.File, row.Killers, got.Name, got.Status, got.Killer, got.Detail, fresh.Status, fresh.Killer)
				return total
			}
			if quiet && fresh.Status == Killed && !strings.HasPrefix(results[k].Detail, "reused:") && !slices.ContainsFunc(row.Killers, func(id string) bool { return strings.HasPrefix(id, "examples:") }) {
				fail(i, "row %s: an edit outside its killers' closures judged it again", row.Name)
				return total
			}
		}
	}
	return total
}
