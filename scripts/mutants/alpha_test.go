package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// alphaSeedVariable replays one seed of TestAlphaAgreesWithAReferenceMatrix.
const alphaSeedVariable = "ERGO_MUTANTS_ALPHA_SEED"

// alphaModule is a generated module whose kill matrix is known by
// construction: package p holds functions F0…Fk-1 (one mutant each) and
// G; each test of p calls a subset of them and checks some of the results
// (a checked call kills its function's mutant, an unchecked one only
// covers it); q's TestQ checks a subset and G, which no test of p calls.
type alphaModule struct {
	funcs   int
	checks  [][]bool // checks[t][i]: p's test t checks Fi
	calls   [][]bool // calls[t][i]: p's test t calls Fi (checked or not)
	qChecks []bool
}

func newAlphaModule(r *rand.Rand) *alphaModule {
	m := &alphaModule{funcs: 3 + r.IntN(3)}
	tests := 3 + r.IntN(3)
	m.qChecks = make([]bool, m.funcs)
	for range tests {
		checks, calls := make([]bool, m.funcs), make([]bool, m.funcs)
		for i := range m.funcs {
			calls[i] = r.IntN(2) == 0
			checks[i] = calls[i] && r.IntN(2) == 0
		}
		m.checks, m.calls = append(m.checks, checks), append(m.calls, calls)
	}
	for i := range m.funcs {
		m.qChecks[i] = r.IntN(3) == 0
	}
	// F0 has two killers (T0 and TestQ), F1 one (T1): the property always
	// sees a shared kill and a unique one.
	m.calls[0][0], m.checks[0][0], m.qChecks[0] = true, true, true
	for t := range tests {
		if t != 1 {
			m.checks[t][1] = false
		}
	}
	m.calls[1][1], m.checks[1][1], m.qChecks[1] = true, true, false
	return m
}

func (m *alphaModule) files() map[string]string {
	var src, ptest, qtest strings.Builder
	src.WriteString("package p\n")
	for i := range m.funcs {
		fmt.Fprintf(&src, "\nfunc F%d(x int) int {\n\treturn x + %d // F%d\n}\n", i, i, i)
	}
	src.WriteString("\nfunc G(x int) int {\n\treturn x * 7 // G\n}\n")
	ptest.WriteString("package p\n\nimport \"testing\"\n")
	for t := range m.checks {
		fmt.Fprintf(&ptest, "\nfunc T%sCase(t *testing.T) {\n", strconv.Itoa(t))
		for i := range m.funcs {
			switch {
			case m.checks[t][i]:
				fmt.Fprintf(&ptest, "\tif F%d(1) != %d {\n\t\tt.Fatal(\"F%d\")\n\t}\n", i, 1+i, i)
			case m.calls[t][i]:
				fmt.Fprintf(&ptest, "\t_ = F%d(1)\n", i)
			}
		}
		ptest.WriteString("}\n")
	}
	qtest.WriteString("package q\n\nimport (\n\t\"testing\"\n\n\t\"fake.test/a/p\"\n)\n\nfunc TestQ(t *testing.T) {\n\tif p.G(1) != 7 {\n\t\tt.Fatal(\"G\")\n\t}\n")
	for i := range m.funcs {
		if m.qChecks[i] {
			fmt.Fprintf(&qtest, "\tif p.F%d(1) != %d {\n\t\tt.Fatal(\"F%d\")\n\t}\n", i, 1+i, i)
		}
	}
	qtest.WriteString("}\n")
	return map[string]string{
		"go.mod":       "module fake.test/a\n\ngo 1.27\n",
		"p/f.go":       src.String(),
		"p/f_test.go":  strings.ReplaceAll(ptest.String(), "func T", "func TestT"),
		"q/q.go":       "package q\n",
		"q/q_test.go":  qtest.String(),
		"p/testdata/x": "",
	}
}

// mutants are a row per function, naming no killer (a covering test
// judges it), and G's row, which names q's TestQ.
func (m *alphaModule) mutants() []*Mutant {
	var out []*Mutant
	for i := range m.funcs {
		out = append(out, &Mutant{Name: fmt.Sprintf("f%d", i), File: "p/f.go", From: fmt.Sprintf("return x + %d // F%d", i, i), To: fmt.Sprintf("return x + %d // F%d", i+1, i)})
	}
	return append(out, &Mutant{Name: "g", File: "p/f.go", From: "return x * 7 // G", To: "return x * 8 // G", Killers: []string{"q:TestQ"}})
}

// reference is each p test's unique and total kills by construction.
func (m *alphaModule) reference() map[string][2]int {
	out := map[string][2]int{}
	for t := range m.checks {
		out["TestT"+strconv.Itoa(t)+"Case"] = [2]int{}
	}
	for i := range m.funcs {
		var killers []string
		for t := range m.checks {
			if m.checks[t][i] {
				killers = append(killers, "TestT"+strconv.Itoa(t)+"Case")
			}
		}
		n := len(killers)
		if m.qChecks[i] {
			n++
		}
		for _, k := range killers {
			c := out[k]
			c[1]++
			if n == 1 {
				c[0]++
			}
			out[k] = c
		}
	}
	return out
}

// Alpha's unique and total kills are the kill matrix's (CS-ALPHA): a
// seeded property over generated modules whose matrix is known by
// construction. For each seed, -alpha's matrix and table over package p
// give every test of p exactly the unique and total kills of the
// reference; G's row, which no test of p covers, is not run; and a second
// pass takes every row from kills.json and judges none. A failing seed
// prints the module, both tables and the replay command.
//
// Mutants (testdata/mutants.json): alpha-stops-at-first-killer,
// alpha-unique-counts-shared, alpha-segment-filter-inverted,
// alpha-matrix-not-reused.
//
// It builds Go because the matrix is the mutant runner's verdicts, and
// the runner's job is to build and run go test binaries on mutated
// copies; no in-process check stands in for that.
func TestAlphaAgreesWithAReferenceMatrix(t *testing.T) {
	seeds, first := 2, uint64(1)
	if s := os.Getenv(alphaSeedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", alphaSeedVariable, s, err)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		checkAlpha(t, seed)
	}
}

func checkAlpha(t *testing.T, seed uint64) {
	m := newAlphaModule(rand.New(rand.NewPCG(seed, 0x616c)))
	repo := filepath.Join(t.TempDir(), "repo")
	files := m.files()
	for name, text := range files {
		writeFile(t, filepath.Join(repo, name), text)
	}
	ledger := filepath.Join(repo, "p/testdata/mutants.json")
	if err := writeLedger(ledger, &Ledger{Mutants: m.mutants()}); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "fake")
	memoryLimit := uint64(0)
	if runtime.GOOS == "linux" {
		memoryLimit = 512 << 20
	}
	store := t.TempDir()
	judged := 0
	var log strings.Builder
	cfg := Config{
		Root:  repo,
		Work:  filepath.Join(t.TempDir(), "work"),
		Cover: []string{"./p"},
		Suites: []Suite{
			{Name: "p", Package: "./p"},
			{Name: "q", Package: "./q", Only: []string{"TestQ"}, Named: []string{"TestQ"}},
		},
		Workers:    2,
		Procs:      1,
		Tests:      2,
		Memory:     memoryLimit,
		CoverStore: store,
		TestFloor:  20 * time.Second,
		Progress:   func(results []Result) { judged = len(results) },
		Log:        &log,
	}
	target := func(id string) bool { return strings.HasPrefix(id, "p:") }
	report := func(what string) {
		t.Helper()
		t.Errorf("seed %d: %s\nmodule:\n%s\n%s\nlog:\n%s\nreplay: %s=%d go test -count=1 -run '^TestAlphaAgreesWithAReferenceMatrix$' ./scripts/mutants",
			seed, what, files["p/f_test.go"], files["q/q_test.go"], log.String(), alphaSeedVariable, seed)
	}
	for pass := 1; pass <= 2; pass++ {
		judged = 0
		l, err := readLedger(ledger)
		if err != nil {
			t.Fatal(err)
		}
		matrix, cov, err := alphaMatrix(context.Background(), cfg, []Job{{Path: ledger, Ledger: l}}, target)
		if err != nil {
			report(fmt.Sprintf("pass %d: alpha: %v", pass, err))
			return
		}
		rows, err := alphaTable(repo, cfg, cov, matrix, []string{"p"}, "")
		if err != nil {
			t.Fatal(err)
		}
		want := m.reference()
		have := map[string][2]int{}
		for _, r := range rows {
			have[r.Test] = [2]int{r.Unique, r.Total}
		}
		if !mapsEqual(have, want) {
			var tbl strings.Builder
			_ = writeAlpha(&tbl, rows)
			report(fmt.Sprintf("pass %d: [unique total] by test %v, want %v\ntable:\n%s", pass, have, want, tbl.String()))
			return
		}
		if slices.ContainsFunc(matrix, func(k killRow) bool { return k.Name == "g" }) {
			report(fmt.Sprintf("pass %d: g, which no test of p covers, is in the matrix", pass))
			return
		}
		if pass == 2 && judged != 0 {
			report(fmt.Sprintf("pass 2 judged %d rows; want every row taken from kills.json", judged))
			return
		}
	}
}

func mapsEqual(a, b map[string][2]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
