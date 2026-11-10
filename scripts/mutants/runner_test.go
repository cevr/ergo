package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// seedVariable replays one seed of TestRunnerMatchesModel.
const seedVariable = "ERGO_MUTANTS_SEED"

// TestRunnerMatchesModel runs the runner over a generated fake module whose
// coverage and kills are known by construction, and compares every verdict
// with the model's. The module has two packages, one and two, each with an
// f.go of the same shape (the same line numbers: coverage read for the
// wrong package runs the wrong tests); each test calls a random set of
// functions and asserts a random part of them; one test fails on the
// unmutated tree. Its ledger holds one value mutant per function (known
// killers drawn at random, some wrong), a mutant that does not build, one
// that no longer applies, a string-literal mutant with an anchor and one
// without, and on Linux one that allocates past the memory limit.
//
// It checks that the runner reports exactly the model's status for each
// mutant, that a killer is a test that asserts the mutated function (with
// All, that the killers are exactly those tests), that the mutant pass runs
// only tests that cover the mutated lines, that a mutant that does not
// build is invalid and never killed, that the memory mutant is killed by
// the memory limit, and that the checkout is left equal to HEAD.
//
// It builds Go because the runner's whole job is to build and run go test
// binaries on mutated copies; no in-process check stands in for that.
func TestRunnerMatchesModel(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	seeds := []uint64{1, 2}
	if s := os.Getenv(seedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, s, err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			checkModel(t, seed, seed%2 == 0)
		})
	}
}

// fakeTest is one generated test: the functions it calls and those of them
// it asserts, as PACKAGE.FUNCTION.
type fakeTest struct {
	suite, name string
	calls       []string
	asserts     []string
	broken      bool
}

func (f fakeTest) id() string { return f.suite + ":" + f.name }

type fakeModule struct {
	funcs []string // PACKAGE.FUNCTION, value funcs only
	tests []fakeTest
}

func value(fn string) int {
	pkg, name, _ := strings.Cut(fn, ".")
	n, _ := strconv.Atoi(strings.TrimPrefix(name, "F"))
	base := 1000
	if pkg == "two" {
		base = 2000
	}
	return base + n
}

func generate(r *rand.Rand) fakeModule {
	var m fakeModule
	n := 3 + r.IntN(3)
	for _, pkg := range []string{"one", "two"} {
		for i := range n {
			m.funcs = append(m.funcs, fmt.Sprintf("%s.F%d", pkg, i))
		}
	}
	for _, pkg := range []string{"one", "two"} {
		for k := range 2 + r.IntN(3) {
			ft := fakeTest{suite: pkg, name: fmt.Sprintf("TestK%d", k)}
			for _, fn := range m.funcs {
				if r.IntN(2) == 0 {
					ft.calls = append(ft.calls, fn)
					if r.IntN(2) == 0 {
						ft.asserts = append(ft.asserts, fn)
					}
				}
			}
			m.tests = append(m.tests, ft)
		}
	}
	m.tests = append(m.tests,
		fakeTest{suite: "one", name: "TestGreet", calls: []string{"one.Greet"}, asserts: []string{"one.Greet"}},
		fakeTest{suite: "one", name: "TestBroken", calls: []string{m.funcs[0]}, asserts: []string{m.funcs[0]}, broken: true},
		fakeTest{suite: "two", name: "TestGrow", calls: []string{"two.Grow"}, asserts: []string{"two.Grow"}},
	)
	return m
}

func (m fakeModule) source(pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	for _, fn := range m.funcs {
		if p, name, _ := strings.Cut(fn, "."); p == pkg {
			fmt.Fprintf(&b, "func %s() int { return %d }\n\n", name, value(fn))
		}
	}
	// The same lines in both files: a greeting and Greet in one, Grow in two.
	if pkg == "one" {
		b.WriteString("var greeting = \"hello\"\n\nvar farewell = \"bye\"\n\nfunc Greet() string { return greeting + farewell }\n")
	} else {
		b.WriteString("func Grow(n int) int { return n }\n")
	}
	return b.String()
}

func (m fakeModule) testSource(pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s_test\n\nimport (\n\t\"testing\"\n\n\t\"fake.test/m/internal/fakelog\"\n\t\"fake.test/m/one\"\n\t\"fake.test/m/two\"\n)\n\nvar _, _ = one.Greet, two.Grow\n\n", pkg)
	for _, ft := range m.tests {
		if ft.suite != pkg {
			continue
		}
		fmt.Fprintf(&b, "func %s(t *testing.T) {\n\tfakelog.Ran(t)\n", ft.name)
		for _, fn := range ft.calls {
			got := fn + "()"
			switch fn {
			case "one.Greet":
				fmt.Fprintf(&b, "\tif %s != \"hellobye\" {\n\t\tt.Fatal(%q)\n\t}\n", got, fn)
				continue
			case "two.Grow":
				fmt.Fprintf(&b, "\tif two.Grow(1) != 1 {\n\t\tt.Fatal(%q)\n\t}\n", fn)
				continue
			}
			if slices.Contains(ft.asserts, fn) {
				fmt.Fprintf(&b, "\tif %s != %d {\n\t\tt.Fatal(%q)\n\t}\n", got, value(fn), fn)
			} else {
				fmt.Fprintf(&b, "\t_ = %s\n", got)
			}
		}
		if ft.broken {
			b.WriteString("\tt.Fatal(\"fails on the unmutated tree\")\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

const fakelog = `package fakelog

import (
	"fmt"
	"os"
	"testing"
)

// Ran notes that a test ran under a mutant.
func Ran(t testing.TB) {
	mutant, path := os.Getenv("ERGO_MUTANT"), os.Getenv("FAKE_LOG")
	if mutant == "" || path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "%s\t%s:%s\n", mutant, os.Getenv("FAKE_SUITE"), t.Name())
	f.Close()
}
`

// expect is the model's verdict on a mutant: its status, the tests that
// kill it, and the tests that cover it (the only ones the runner may run).
type expect struct {
	status   string
	killers  []string
	covering []string
	reason   string
}

func (m fakeModule) model(target string, invalid bool) expect {
	var e expect
	for _, ft := range m.tests {
		if ft.broken || !slices.Contains(ft.calls, target) {
			continue
		}
		e.covering = append(e.covering, ft.id())
		if slices.Contains(ft.asserts, target) {
			e.killers = append(e.killers, ft.id())
		}
	}
	switch {
	case len(e.covering) == 0:
		e.status = Uncovered
	case invalid:
		e.status, e.killers = Invalid, nil
	case len(e.killers) > 0:
		e.status = Killed
	default:
		e.status = Survived
	}
	return e
}

func checkModel(t *testing.T, seed uint64, all bool) {
	r := rand.New(rand.NewPCG(seed, 0x6d75))
	m := generate(r)
	repo := filepath.Join(t.TempDir(), "repo")
	files := map[string]string{
		"go.mod":                      "module fake.test/m\n\ngo 1.27\n",
		"internal/fakelog/fakelog.go": fakelog,
		"one/f.go":                    m.source("one"),
		"two/f.go":                    m.source("two"),
		"one/f_test.go":               m.testSource("one"),
		"two/f_test.go":               m.testSource("two"),
	}
	for name, text := range files {
		writeFile(t, filepath.Join(repo, name), text)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "fake")

	var mutants []*Mutant
	want := map[string]expect{}
	var ids []string
	for _, ft := range m.tests {
		ids = append(ids, ft.id())
	}
	for _, fn := range m.funcs {
		pkg, name, _ := strings.Cut(fn, ".")
		v := value(fn)
		mu := &Mutant{Name: "value-" + fn, File: pkg + "/f.go", From: fmt.Sprintf("return %d }", v), To: fmt.Sprintf("return %d }", v+1), Rule: name + " returns its value"}
		for range r.IntN(3) {
			mu.Killers = append(mu.Killers, ids[r.IntN(len(ids))])
		}
		mutants = append(mutants, mu)
		want[mu.Name] = m.model(fn, false)
	}
	extra := []struct {
		mu   *Mutant
		want expect
	}{
		{&Mutant{Name: "does-not-build", File: "one/f.go", From: "return greeting + farewell }", To: "return 1 }"}, m.model("one.Greet", true)},
		{&Mutant{Name: "no-longer-applies", File: "one/f.go", From: "return 9999 }", To: "return 1 }"}, expect{status: Stale}},
		{&Mutant{Name: "literal-anchored", File: "one/f.go", From: `"hello"`, To: `"hullo"`, Anchor: &Anchor{Text: "return greeting + farewell"}}, m.model("one.Greet", false)},
		{&Mutant{Name: "literal-bare", File: "one/f.go", From: `"bye"`, To: `"by"`}, expect{status: Uncovered}},
	}
	memoryLimit := uint64(0)
	if runtime.GOOS == "linux" {
		// The kernel enforces RLIMIT_DATA on Linux only (internal/proclimit).
		memoryLimit = 512 << 20
		grow := m.model("two.Grow", false)
		grow.reason = "memory"
		extra = append(extra, struct {
			mu   *Mutant
			want expect
		}{&Mutant{Name: "grows", File: "two/f.go", From: "func Grow(n int) int { return n }",
			To: "func Grow(n int) int {\n\tb := make([]byte, 1<<30)\n\tfor i := 0; i < len(b); i += 4096 {\n\t\tb[i] = 1\n\t}\n\treturn n + int(b[0])\n}"}, grow})
	}
	for _, x := range extra {
		x.mu.Rule = "a fixed case"
		mutants = append(mutants, x.mu)
		want[x.mu.Name] = x.want
	}

	logPath := filepath.Join(t.TempDir(), "ran.log")
	t.Setenv("FAKE_LOG", logPath)
	cfg := Config{
		Root:    repo,
		Work:    filepath.Join(t.TempDir(), "work"),
		Cover:   []string{"./one", "./two"},
		Suites:  []Suite{{Name: "one", Package: "./one", Env: []string{"FAKE_SUITE=one"}}, {Name: "two", Package: "./two", Env: []string{"FAKE_SUITE=two"}}},
		Workers: 2,
		Procs:   1,
		Memory:  memoryLimit,
		All:     all,
	}
	results, _, err := Run(context.Background(), cfg, []Job{{Path: filepath.Join(repo, "one/testdata/mutants.json"), Ledger: &Ledger{Mutants: mutants}}})
	if err != nil {
		t.Fatalf("seed %d: run: %v", seed, err)
	}
	if len(results) != len(mutants) {
		t.Fatalf("seed %d: %d results for %d mutants", seed, len(results), len(mutants))
	}
	ran := map[string][]string{}
	logText, _ := os.ReadFile(logPath)
	for _, line := range strings.Split(strings.TrimSpace(string(logText)), "\n") {
		if mutant, test, ok := strings.Cut(line, "\t"); ok {
			ran[mutant] = append(ran[mutant], test)
		}
	}
	for i, res := range results {
		mu := mutants[i]
		w := want[mu.Name]
		bad := func(format string, args ...any) {
			t.Errorf("seed %d (replay: %s=%d), mutant %s: %s\n  model: %+v\n  runner: status %s killer %s (%s) killers %v ran %v\n  %s", seed, seedVariable, seed, mu.Name,
				fmt.Sprintf(format, args...), w, res.Status, res.Killer, res.Reason, res.Killers, res.Ran, firstLine(res.Detail))
		}
		if res.Name != mu.Name {
			bad("result %d is %s", i, res.Name)
		}
		if res.Status != w.status {
			bad("status %s, model %s", res.Status, w.status)
		}
		if res.Status == Killed && !slices.Contains(w.killers, res.Killer) {
			bad("killer %s does not assert the mutated function", res.Killer)
		}
		if all && res.Status == Killed && !sameSet(res.Killers, w.killers) {
			bad("killers %v, model %v", res.Killers, w.killers)
		}
		if w.reason != "" && res.Reason != w.reason {
			bad("killed by %q, want %q", res.Reason, w.reason)
		}
		for _, test := range append(slices.Clone(res.Ran), ran[mu.Name]...) {
			if !slices.Contains(w.covering, test) {
				bad("ran %s, which does not cover the mutated lines", test)
			}
		}
	}
	if status := git(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("seed %d: the checkout differs from HEAD after the run:\n%s", seed, status)
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
