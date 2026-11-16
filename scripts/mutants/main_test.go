package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestUnjudgedFailsTheRun is the smallest repro of FT11b's cmd/ergo run,
// where 7 tests failed on the unmutated tree and the run judged their rows
// uncovered: a row a red test would judge gets no verdict, and the run
// fails and lists it. So does an invalid row (GT-COMPILE: rows "killed"
// only by a compile error). Mutants: gt8b-unjudged-exit, gtc-invalid-exit.
func TestUnjudgedFailsTheRun(t *testing.T) {
	for _, c := range []struct{ status, detail, list string }{
		{Unjudged, "s:TestRed fails on the unmutated tree", "NOT JUDGED"},
		{Invalid, "does not compile: f.go:3:1: x", "row (invalid, origin o): does not compile"},
	} {
		m := &Mutant{Name: "row", Origin: "o", File: "f.go", Killers: []string{"s:TestRed"}}
		jobs := []Job{{Path: "l.json", Ledger: &Ledger{Mutants: []*Mutant{m}}}}
		results := []Result{{Ledger: "l.json", Name: "row", Status: c.status, Detail: c.detail}}
		cov := &coverage{Tests: []*covTest{{ID: "s:TestRed", Failed: c.status == Unjudged, Detail: "fail"}}}
		cov.reindex()
		var out strings.Builder
		if code := report(&out, jobs, results, cov); code != 1 || !strings.Contains(out.String(), c.list) {
			t.Errorf("a row %s: exit %d, want 1, and %q listed:\n%s", c.status, code, c.list, out.String())
		}
	}
}

// TestHeaderLineIsCoveredByWhatReachesIt is the smallest repro of M1's
// uncovered runtime/v1 mutant: the cover tool gives a case line of a
// tagless switch no block (measured: blocks 180 and 182 around the case at
// 181 of http_client.go), so a mutant there had no covering test. The
// block that ends last before the line stands for it.
func TestHeaderLineIsCoveredByWhatReachesIt(t *testing.T) {
	c := &coverage{
		Tests: []*covTest{{ID: "s:Early"}, {ID: "s:Reaches"}, {ID: "s:Body"}},
		Files: map[string][]covBlock{"f.go": {{Start: 1, End: 3, Tests: []int{0}}, {Start: 5, End: 6, Tests: []int{1}}, {Start: 8, End: 8, Tests: []int{2}}}},
	}
	c.reindex()
	header := headerLine("a\nb\n\t\tcase errors.Is(err, x):\n", len("a\nb\n\t\tcase "))
	got := c.covering("f.go", 7, 7, header, nil)
	if !header || len(got) != 1 || !got["s:Reaches"] {
		t.Errorf("the tests of a header line at 7 (header %v): %v, want s:Reaches", header, got)
	}
	if other := c.covering("f.go", 7, 7, headerLine("var x = \"lit\"\n", 9), nil); len(other) != 0 {
		t.Errorf("a line in no block that is no header is covered by %v", other)
	}
}

// TestDefaultWorkIsPrivate is the smallest repro of batch 18's shared
// scratch: two runs at once with the default -work shared
// /tmp/ergo-mutants, and one run's coverage pass removed the other's
// counters ("open /tmp/ergo-mutants/cover/counters/37: no such file").
// Each run's default is its own directory, removed at the end (mutants
// mutclean-work-shared, mutclean-work-kept).
func TestDefaultWorkIsPrivate(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	a, doneA, err := privateWork()
	if err != nil {
		t.Fatal(err)
	}
	b, doneB, err := privateWork()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two runs share the work directory %s", a)
	}
	doneA()
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("the run's work directory %s is left after the run: %v", a, err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Errorf("ending one run removed another's work directory: %v", err)
	}
	doneB()
}

// TestScopeIsTheLedgersPackage: a ledger outside check and lower is judged
// by its own package's tests alone, so the coverage pass records that
// package's blocks and runs only its tests (M1: under ergoSuites a
// runtime/v1 mutant was uncovered, after a pass over every check, lower and
// std test and every example). The pass's work is the suites and tools it
// runs, counted here; check and lower keep ergoSuites, and two ledgers
// take the union, each suite once.
func TestScopeIsTheLedgersPackage(t *testing.T) {
	root := t.TempDir()
	var own Config
	addScope(&own, scopeOf(root, ledgerPath(root, filepath.Join(root, "runtime", "v1"))))
	if !slices.Equal(own.Cover, []string{"./runtime/v1"}) || len(own.Tools) != 0 || len(own.Suites) != 1 || own.Suites[0].Name != "runtime/v1" || own.Suites[0].Package != "./runtime/v1" {
		t.Errorf("runtime/v1's scope: cover %q, %d tools, suites %+v", own.Cover, len(own.Tools), own.Suites)
	}
	var both Config
	addScope(&both, scopeOf(root, ledgerPath(root, "lower")))
	addScope(&both, scopeOf(root, ledgerPath(root, "check")))
	addScope(&both, scopeOf(root, ledgerPath(root, filepath.Join(root, "runtime", "v1"))))
	cover, tools, suites := ergoSuites()
	var names []string
	for _, s := range both.Suites {
		names = append(names, s.Name)
	}
	want := []string{}
	for _, s := range suites {
		want = append(want, s.Name)
	}
	want = append(want, "runtime/v1")
	if !slices.Equal(both.Cover, append(cover, "./runtime/v1")) || len(both.Tools) != len(tools) || !slices.Equal(names, want) {
		t.Errorf("lower, check and runtime/v1: cover %q, %d tools, suites %q; want suites %q", both.Cover, len(both.Tools), names, want)
	}
}

// TestKillerInAnotherSuiteIsRun: a ledger's killer in a suite its scope
// does not bring is run, and only the tests the ledger names. DIR[TAGS]
// is DIR built with -tags=TAGS (runtime/v1's ergoexplore tests, whose
// other tests hang without the explored worker: 4 h of deadlines); a
// module directory is that package (eval's row killed by
// internal/sketchfront, NONDET1); an ergo suite name is that suite, with
// the tools it runs (lower runs the ergo CLI). A name that is neither is
// refused: 45 rows named v1:, main:, sched:, tsproj: … and were never run
// (fu-mutants-xpkg). Every suite records the tests the ledger names
// (Named), a note after the test is dropped, and only the selected rows
// bring suites: the run's work is the suites it builds, counted here.
// Before, the x7 rows were judged by the untagged binary, where their
// killers do not exist, and the other rows were run by hand. An id that
// names no suite, or no test of its suite, is an error: the runner skipped
// it before, and rows whose killers were v1:, sched: or rt-explore: ran
// without them (MUT-names).
func TestKillerInAnotherSuiteIsRun(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"runtime/v1", "internal/sketchfront", "examples/demo/memory"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for f, text := range map[string]string{
		"runtime/v1/v1.go":                "",
		"runtime/v1/v1_test.go":           "func TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\nfunc TestC(t *testing.T) {}\nfunc TestOwn(t *testing.T) {}\n",
		"internal/sketchfront/f_test.go":  "func TestF(t *testing.T) {}\n",
		"internal/sketch/lower/l_test.go": "func TestL(t *testing.T) {}\n",
		"examples/demo/memory/demo.ergo":  "",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An example's ledger edits .ergo files: it is no Go package and no
	// suite, and is judged by the killer it names alone.
	demo := &Ledger{Mutants: []*Mutant{{Name: "e", File: "examples/demo/memory/demo.ergo", Killers: []string{"examples:demo"}}}}
	cover, _, suites, err := suitesFor(root, []Job{{Path: ledgerPath(root, filepath.Join(root, "examples", "demo")), Ledger: demo}})
	if err != nil || len(cover) != 0 || len(suites) != 1 || suites[0].Name != "examples" || !slices.Equal(suites[0].Named, []string{"demo"}) {
		t.Errorf("an example's ledger: cover %q, suites %+v, err %v; want only examples running demo", cover, suites, err)
	}
	// A killer in a nested module (internal/selfhostcheck/testdata/selfhost,
	// batch-18 r4 M2) is built in that module, and its package is covered
	// by no -coverpkg: go test from the root refuses both.
	for f, text := range map[string]string{"nested/m/go.mod": "module nested.test/m\n", "nested/m/p/p.go": "package p\n", "nested/m/p/p_test.go": "package p\n\nfunc TestN(t *testing.T) {}\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nested := &Ledger{Mutants: []*Mutant{{Name: "n", File: "nested/m/p/p.go", Killers: []string{"nested/m/p:TestN"}}}}
	cover, _, suites, err = suitesFor(root, []Job{{Path: ledgerPath(root, filepath.Join(root, "examples", "demo")), Ledger: nested}})
	if err != nil || len(cover) != 0 || len(suites) != 1 || suites[0].Module != "nested/m" || suites[0].Package != "./p" {
		t.Errorf("a nested module's killer: cover %q, suites %+v, err %v; want the suite ./p of module nested/m and no cover package", cover, suites, err)
	}
	ledger := &Ledger{Mutants: []*Mutant{
		{Name: "a", Killers: []string{"runtime/v1[ergoexplore]:TestA", "runtime/v1:TestB", "internal/sketchfront:TestF"}},
		{Name: "b", Killers: []string{"lower:TestL", "runtime/v1:TestOwn (a note)"}, Anchor: &Anchor{Tests: []string{"runtime/v1[ergoexplore]:TestC"}}},
		{Name: "c", Killers: []string{"internal/closure:TestOther"}},
	}}
	at := ledgerPath(root, filepath.Join(root, "runtime", "v1"))
	_, tools, suites, err := suitesFor(root, []Job{{Path: at, Ledger: ledger, Select: regexp.MustCompile("^[ab]$")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"v1:TestA", "runtime/v1:TestNone", "lower:TestNone", "examples:nowhere", "runtime/v1[ergoexplore:TestA"} {
		bad := &Ledger{Mutants: []*Mutant{{Name: "c", Killers: []string{"runtime/v1:TestB", id}}}}
		if _, _, _, err := suitesFor(root, []Job{{Path: at, Ledger: bad}}); err == nil || !strings.Contains(err.Error(), id) {
			t.Errorf("killer %q: error %v, want one that names it", id, err)
		}
	}
	type got struct {
		name, pkg, tags string
		only, named     []string
	}
	var have []got
	for _, s := range suites {
		have = append(have, got{s.Name, s.Package, s.Tags, s.Only, s.Named})
	}
	want := []got{
		{"runtime/v1", "./runtime/v1", "", nil, []string{"TestB", "TestOwn"}},
		{"runtime/v1[ergoexplore]", "./runtime/v1", "ergoexplore", []string{"TestA", "TestC"}, []string{"TestA", "TestC"}},
		{"internal/sketchfront", "./internal/sketchfront", "", []string{"TestF"}, []string{"TestF"}},
		{"lower", "./internal/sketch/lower", "", []string{"TestL"}, []string{"TestL"}},
	}
	same := len(have) == len(want)
	for i := 0; same && i < len(want); i++ {
		same = have[i].name == want[i].name && have[i].pkg == want[i].pkg && have[i].tags == want[i].tags && slices.Equal(have[i].only, want[i].only) && slices.Equal(have[i].named, want[i].named)
	}
	if !same {
		t.Errorf("suites %+v, want %+v (row c is not selected: internal/closure is not built)", have, want)
	}
	if len(tools) != 1 || tools[0].Name != "ergo" {
		t.Errorf("tools %+v, want the ergo CLI that lower runs", tools)
	}
	ledger.Mutants = append(ledger.Mutants, &Mutant{Name: "d", Killers: []string{"v1:TestNone"}})
	if _, _, _, err := suitesFor(root, []Job{{Path: at, Ledger: ledger}}); err == nil || !strings.Contains(err.Error(), "v1:TestNone") {
		t.Errorf("a killer in no suite: err %v, want it named", err)
	}
}

// TestLedgerEnvReachesItsOwnSuite: a package ledger's env is the
// environment of its own suite, where its killers run, and of no other
// suite. Before, tsproj's TS-leg killers skipped unless the person running
// the runner remembered ERGO_TS_TEST=1, and 36 of its 62 rows went
// unjudged (MUT-names r1). check and lower share the ergo suites, so an
// env in their ledgers is refused rather than leaking into every suite.
func TestLedgerEnvReachesItsOwnSuite(t *testing.T) {
	root := t.TempDir()
	// Only a package with tests is a suite (suitesFor), so tsproj has one.
	for _, f := range []string{"internal/sketch/lower/l_test.go", "internal/tsproj/p_test.go"} {
		path := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("func TestL(t *testing.T) {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"ERGO_TS_TEST=1"}
	own := &Ledger{Env: env, Mutants: []*Mutant{{Name: "a", Killers: []string{"lower:TestL"}}}}
	_, _, suites, err := suitesFor(root, []Job{{Path: ledgerPath(root, filepath.Join(root, "internal", "tsproj")), Ledger: own}})
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) < 2 || suites[0].Name != "internal/tsproj" || !slices.Equal(suites[0].Env, env) {
		t.Fatalf("suites %+v: the ledger's own suite comes first with its env", suites)
	}
	for _, s := range suites[1:] {
		if slices.Contains(s.Env, env[0]) {
			t.Errorf("suite %s got the tsproj ledger's env: %q", s.Name, s.Env)
		}
	}
	if _, _, _, err := suitesFor(root, []Job{{Path: ledgerPath(root, "lower"), Ledger: &Ledger{Env: env}}}); err == nil {
		t.Error("lower's ledger took an env")
	}
}
