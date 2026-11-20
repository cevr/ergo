package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// seedVariable replays one seed of TestRunnerMatchesModel.
const seedVariable = "ERGO_MUTANTS_SEED"

// TestRunnerMatchesModel runs the runner over a generated fake module whose
// coverage and kills are known by construction, and compares every verdict
// with the model's. The module has two packages, one and two, each with an
// f.go of the same shape (the same line numbers: coverage read for the
// wrong package runs the wrong tests); each test calls a random set of
// functions and asserts a random part of them; one test fails on the
// unmutated tree, so a mutant it covers or that names it is unjudged
// (FT11b: a red baseline judges nothing). Its ledger holds one value mutant per function (known
// killers drawn at random, some wrong), a mutant that does not build, a
// mutant of a package no test binary imports that no longer compiles, whose
// named killer builds that package (invalid, not killed: GT-COMPILE), one
// that no longer applies, a string-literal mutant with an anchor and one
// without (no block of one runs it, but two:TestReadsOne reads one/f.go:
// its test log selects it for every mutant of that file, and it kills this
// one, GT8), the same mutant named with that killer (fu-mutants-xpkg),
// mutants of a data file (one/word.txt, as std's .ergo sources) that the
// tests reading it judge, with those their rows name, and on Linux one
// that allocates past the memory limit. A named killer runs whether or not
// it covers the lines. TestGreet
// also reads the repository with git ls-files, as internal/residue's rules
// do: in a copy with no Git index it would fail on the unmutated tree; and
// it fails when GOFLAGS is set, as cmd/ergo's direct-path tests do.
// TestPlain fails when its binary is built with coverage, as
// internal/closure's sandboxed self-runs do, and when it has no deadline,
// as internal/closure's reap test does; it is the only killer of
// plain-only, which is uncovered unless the coverage pass reruns it
// without coverage and with a -test.timeout. Mutants of a source file that
// a source check validates (Config.Sources; a stand-in for .ergo): a value
// change, one the formatter would rewrite (FK7a), one the compiler refuses
// (FK12a), and that refusal declared as the row's point.
//
// It checks that the runner reports exactly the model's status for each
// mutant, that a killer is a test that asserts the mutated function (with
// All, that the killers are exactly those tests), that the mutant pass runs
// only tests that cover the mutated lines, that a mutant that does not
// build is invalid and never killed, that the memory mutant is killed by
// the memory limit, and that the checkout is left equal to HEAD. Seed 1
// runs a second pass over the coverage store the first filled (GT6): it
// reuses two's segment, measures one again, and its verdicts are the
// model's too. Mutants of the runner it catches: fk7a-format-ignored,
// fk12a-refusal-ignored, fk12a-declared-ignored (this package's ledger).
//
// GT-BOUND: a row its named tests kill is judged with no coverage (seed
// 1's third pass selects two such rows and measures nothing), and a named
// kill stands though a covering test is red; spins is hung (its only test
// blocks until its -test.timeout); data-command is killed by a command
// killer; seed 2 stops a run after four verdicts and the next run takes
// them from the journal, runs nothing on those rows and removes the
// journal at its end (gtb-named-skipped, gtb-panic-ignored,
// gtb-journal-unread, gtb-journal-kept, gtb-command-words).
//
// GT-NOTESTS: dies-before-tests makes every binary that links one panic in
// its init, before any test starts: its tests fail, but none ran, so it is
// invalid, not killed (gtn-started-ignored).
//
// GT-FAMILY: two:TestFam is no test but a family split into parts
// (TestFamPart0, TestFamPart1, as irspike's TestIRAgreesOnExamples is):
// family-named names the family, which runs every part, and its
// failing part kills it in the named pass (gtf-family-one-part,
// gtf-family-start-unseen).
//
// Batch 23 fallout: regen-breaks-build's regenerate step writes a
// two/names.go that its named killer's suite cannot build, a kill;
// regen-step-fails, regen-source-does-not-compile and names-renamed (the
// same build failure with no step) stay invalid (rgk-any-row,
// rgk-detail-first-line); regen-unnamed-build, whose step breaks a suite
// the row does not name, stays invalid (rgk-unnamed-killer).
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
	// plainOnly fails when its binary is built with coverage, as
	// internal/closure's sandboxed self-runs do, or when it has no
	// deadline (go test always passes -test.timeout); it passes without
	// coverage and with a deadline.
	plainOnly bool
	reads     bool // asserts the data file one/word.txt
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
		fakeTest{suite: "two", name: "TestPlain", calls: []string{"two.Plain"}, asserts: []string{"two.Plain"}, plainOnly: true},
		fakeTest{suite: "one", name: "TestWord", reads: true},
		fakeTest{suite: "two", name: "TestSpin", calls: []string{"two.Spin"}},
		fakeTest{suite: "two", name: "TestFamPart0"},
		fakeTest{suite: "two", name: "TestFamPart1", calls: []string{"two.Fam"}, asserts: []string{"two.Fam"}},
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
		b.WriteString("func Grow(n int) int { return n }\n\nfunc Plain() int { return 7 }\n\nfunc Spin() int { return 3 }\n\nfunc Fam() int { return 5 }\n")
	}
	return b.String()
}

func (m fakeModule) testSource(pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s_test\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n\t\"strings\"\n\t\"testing\"\n\n\t\"fake.test/m/internal/fakelog\"\n\t\"fake.test/m/one\"\n\t\"fake.test/m/two\"\n)\n\nvar _, _, _, _, _ = one.Greet, two.Grow, exec.Command, os.ReadFile, strings.Contains\n\n", pkg)
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
				// It reads the repository, as internal/residue's rules do:
				// the copy must carry the checkout's file list.
				b.WriteString("\tif out, err := exec.Command(\"git\", \"ls-files\", \"--error-unmatch\", \"f.go\").CombinedOutput(); err != nil {\n\t\tt.Fatalf(\"git ls-files: %v\\n%s\", err, out)\n\t}\n")
				// It sees the go command's settings as the gate sets them:
				// a set GOFLAGS makes ergo build refuse its direct path.
				b.WriteString("\tif f := os.Getenv(\"GOFLAGS\"); f != \"\" {\n\t\tt.Fatalf(\"GOFLAGS=%q\", f)\n\t}\n")
				// It finds the module's toolchain first on PATH, as go test
				// puts it: ergo build refuses another go (opt9's killer).
				b.WriteString("\tif out, err := exec.Command(\"go\", \"env\", \"GOROOT\").Output(); err != nil || strings.Split(os.Getenv(\"PATH\"), string(os.PathListSeparator))[0] != strings.TrimSpace(string(out))+string(os.PathSeparator)+\"bin\" {\n\t\tt.Fatalf(\"the toolchain is not first on PATH %q (%s, %v)\", os.Getenv(\"PATH\"), out, err)\n\t}\n")
				continue
			case "two.Grow":
				fmt.Fprintf(&b, "\tif two.Grow(1) != 1 {\n\t\tt.Fatal(%q)\n\t}\n", fn)
				continue
			case "two.Fam":
				fmt.Fprintf(&b, "\tif two.Fam() != 5 {\n\t\tt.Fatal(%q)\n\t}\n", fn)
				continue
			case "two.Plain":
				fmt.Fprintf(&b, "\tif two.Plain() != 7 {\n\t\tt.Fatal(%q)\n\t}\n", fn)
				continue
			case "two.Spin":
				// A changed Spin blocks it until its -test.timeout.
				b.WriteString("\tif two.Spin() != 3 {\n\t\tselect {}\n\t}\n")
				continue
			}
			if slices.Contains(ft.asserts, fn) {
				fmt.Fprintf(&b, "\tif %s != %d {\n\t\tt.Fatal(%q)\n\t}\n", got, value(fn), fn)
			} else {
				fmt.Fprintf(&b, "\t_ = %s\n", got)
			}
		}
		if ft.reads {
			b.WriteString("\tif b, _ := os.ReadFile(\"word.txt\"); string(b) != \"hi\\n\" {\n\t\tt.Fatal(\"word.txt\")\n\t}\n")
		}
		if ft.broken {
			b.WriteString("\tt.Fatal(\"fails on the unmutated tree\")\n")
		}
		if ft.plainOnly {
			b.WriteString("\tif _, ok := t.Deadline(); !ok {\n\t\tt.Fatal(\"no -test.timeout, unlike go test\")\n\t}\n")
			b.WriteString("\tif testing.CoverMode() != \"\" {\n\t\tt.Fatal(\"fails when built with coverage\")\n\t}\n")
		}
		b.WriteString("}\n\n")
	}
	if pkg == "two" {
		// It kills a mutant of one that no coverage of it records, as a
		// test that builds and runs a program does (check's tests kill
		// eval's mutants through the ergo programs they run).
		b.WriteString("func TestReadsOne(t *testing.T) {\n\tfakelog.Ran(t)\n\tsrc, err := os.ReadFile(\"../one/f.go\")\n\tif err != nil || !strings.Contains(string(src), `\"bye\"`) {\n\t\tt.Fatal(\"one's farewell changed\", err)\n\t}\n}\n\n")
	}
	return b.String()
}

// genTest builds package gen, which no test binary imports.
const genTest = `package two_test

import (
	"os/exec"
	"testing"

	"fake.test/m/internal/fakelog"
)

func TestBuildsGen(t *testing.T) {
	fakelog.Ran(t)
	cmd := exec.Command("go", "build", "./gen")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./gen: %v\n%s", err, out)
	}
}
`

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

// srcTest reads one/data.src as the examples runner reads a program: it
// fails when the file is not formatted (a double space), when the
// compiler would refuse it (ERROR), or when its value is not 1.
const srcTest = `package one_test

import (
	"os"
	"strings"
	"testing"

	"fake.test/m/internal/fakelog"
)

func TestSrc(t *testing.T) {
	fakelog.Ran(t)
	data, err := os.ReadFile("data.src")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	switch {
	case strings.Contains(text, "  "):
		t.Fatal("not formatted")
	case strings.Contains(text, "ERROR"):
		t.Fatal("refused")
	case text != "value 1\n":
		t.Fatalf("value %q", text)
	}
}
`

// srcTool is the source check's tool: "fmt FILE" fails on a double space,
// "check DIR" prints one E1 diagnostic per ERROR in DIR's .src files.
// regenNames is the regenerate step of two/names.go: it declares the
// name two/names.txt holds, as go generate writes drawgen's packages.
var regenNames = &Regenerate{Dir: "two", Argv: []string{"sh", "-c", `printf 'package two\n\nconst %s = 7\n' "$(cat names.txt)" > names.go`}}

const srcTool = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	switch os.Args[1] {
	case "fmt":
		data, err := os.ReadFile(os.Args[2])
		if err != nil || strings.Contains(string(data), "  ") {
			os.Exit(1)
		}
	case "check":
		files, _ := filepath.Glob(filepath.Join(os.Args[2], "*.src"))
		var codes []string
		for _, f := range files {
			data, _ := os.ReadFile(f)
			for range strings.Count(string(data), "ERROR") {
				codes = append(codes, ` + "`" + `{"code":"E1"}` + "`" + `)
			}
		}
		fmt.Printf("{\"diagnostics\":[%s]}\n", strings.Join(codes, ","))
	}
}
`

var srcAnchor = &Anchor{Tests: []string{"one:TestSrc"}}

func srcExpect(status string) expect {
	e := expect{status: status, covering: []string{"one:TestSrc"}}
	if status == Killed {
		e.killers = e.covering
	}
	return e
}

// expect is the model's verdict on a mutant: its status, the tests that
// kill it, and the tests that cover it (the only ones the runner may run).
type expect struct {
	status   string
	killers  []string
	covering []string
	reason   string
	detail   string // a prefix of the runner's detail
}

func (m fakeModule) model(target string, invalid bool) expect {
	var e expect
	red := false
	for _, ft := range m.tests {
		if ft.broken && slices.Contains(ft.calls, target) {
			red = true
		}
		if ft.broken || !slices.Contains(ft.calls, target) {
			continue
		}
		e.covering = append(e.covering, ft.id())
		if slices.Contains(ft.asserts, target) {
			e.killers = append(e.killers, ft.id())
		}
	}
	switch {
	case red:
		// A covering test fails on the unmutated tree: no verdict, and
		// nothing runs.
		e.status, e.killers = Unjudged, nil
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

// named adds the ledger's named killers of a mutant of target to e: the
// runner runs a named test whether or not it covers the lines, so a mutant
// whose named test does not kill it survives rather than being uncovered.
// A named test that fails on the unmutated tree leaves the mutant
// unjudged. A named test that kills it judges it before any coverage
// (phase A), so a covering test that fails on the unmutated tree no longer
// matters: the kill rests on a test that passes there.
func (m fakeModule) named(e expect, target string, killers []string) expect {
	broken, kills := false, false
	for _, id := range killers {
		i := slices.IndexFunc(m.tests, func(ft fakeTest) bool { return ft.id() == id })
		if i < 0 {
			continue
		}
		if m.tests[i].broken {
			broken = true
			continue
		}
		if !slices.Contains(e.covering, id) {
			e.covering = append(e.covering, id)
		}
		kills = kills || slices.Contains(m.tests[i].asserts, target)
	}
	switch {
	case broken:
		e.status, e.killers = Unjudged, nil
	case kills:
		e.status, e.killers = Killed, nil
		for _, ft := range m.tests {
			if !ft.broken && slices.Contains(ft.asserts, target) {
				e.killers = append(e.killers, ft.id())
			}
		}
	case e.status == Uncovered:
		e.status = Survived
	}
	return e
}

// read adds a test that reads the mutated file and does not kill the
// mutant to e: it covers the mutant, so an uncovered one survives.
func (m fakeModule) read(e expect, reader string) expect {
	if slices.Contains(e.covering, reader) {
		return e
	}
	e.covering = append(e.covering, reader)
	if e.status == Uncovered {
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
		// No test binary imports gen; TestBuildsGen builds it, as lower's
		// tests build a lowered program against runtime/v1.
		"gen/g.go":        "package gen\n\nfunc G() int { return 1 }\n",
		"two/gen_test.go": genTest,
		"one/data.src":    "value 1\n",
		"one/src_test.go": srcTest,
		"cmd/src/main.go": srcTool,
		"one/word.txt":    "hi\n",
		// two/names.go is generated from two/names.txt (regenNames), and
		// TestNames uses the name it declares.
		"two/names.txt":     "Seven\n",
		"two/names.go":      "package two\n\nconst Seven = 7\n",
		"two/names_test.go": "package two\n\nimport \"testing\"\n\nfunc TestNames(t *testing.T) {\n\tif Seven != 7 {\n\t\tt.Fatal(Seven)\n\t}\n}\n",
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
		want[mu.Name] = m.named(m.model(fn, false), fn, mu.Killers)
	}
	extra := []struct {
		mu   *Mutant
		want expect
	}{
		{&Mutant{Name: "does-not-build", File: "one/f.go", From: "return greeting + farewell }", To: "return 1 }"}, m.model("one.Greet", true)},
		// Every test binary that links one dies in its init, before any
		// test starts, as a test command that errors before it runs one
		// does (Lane A's S2e run): a failure, but no test ran, so no kill
		// (GT-NOTESTS).
		{&Mutant{Name: "dies-before-tests", File: "one/f.go", From: "func Greet() string { return greeting + farewell }", To: "func Greet() string { return greeting + farewell }\n\nfunc init() { panic(\"before any test\") }"},
			m.model("one.Greet", true)},
		{&Mutant{Name: "no-longer-applies", File: "one/f.go", From: "return 9999 }", To: "return 1 }"}, expect{status: Stale}},
		{&Mutant{Name: "literal-anchored", File: "one/f.go", From: `"hello"`, To: `"hullo"`, Anchor: &Anchor{Text: "return greeting + farewell"}}, m.model("one.Greet", false)},
		// No block of one's runs in two:TestReadsOne, but it reads one/f.go
		// (its test log, reads.go), so it covers the file and kills.
		{&Mutant{Name: "literal-bare", File: "one/f.go", From: `"bye"`, To: `"by"`},
			expect{status: Killed, killers: []string{"two:TestReadsOne"}, covering: []string{"two:TestReadsOne"}}},
		// The ledger names two:TestReadsOne too (with a note for the
		// reader): it runs and kills.
		{&Mutant{Name: "literal-named", File: "one/f.go", From: `"bye"`, To: `"by"`, Killers: []string{"two:TestReadsOne (reads one's source)"}},
			expect{status: Killed, killers: []string{"two:TestReadsOne"}, covering: []string{"two:TestReadsOne"}}},
		// Its named test fails only because gen no longer compiles: the
		// suites build, and the mutant is invalid, not killed (GT-COMPILE).
		{&Mutant{Name: "gen-does-not-compile", File: "gen/g.go", From: "return 1 }", To: "return \"x\" }", Killers: []string{"two:TestBuildsGen"}},
			expect{status: Invalid, detail: "does not compile: gen/g.go:3:"}},
		// Its only covering test blocks until its -test.timeout: no kill,
		// and a hung row (GT-BOUND).
		{&Mutant{Name: "spins", File: "two/f.go", From: "func Spin() int { return 3 }", To: "func Spin() int { return 4 }"},
			expect{status: Hung, covering: []string{"two:TestSpin"}, detail: "two:TestSpin fail (a timeout, no kill)"}},
		{&Mutant{Name: "plain-only", File: "two/f.go", From: "func Plain() int { return 7 }", To: "func Plain() int { return 8 }"}, m.model("two.Plain", false)},
		// The row names the family; its named pass runs both parts, and
		// TestFamPart1 fails (GT-FAMILY). With -all the coverage pass runs
		// the part that covers Fam too.
		{&Mutant{Name: "family-named", File: "two/f.go", From: "func Fam() int { return 5 }", To: "func Fam() int { return 6 }", Killers: []string{"two:TestFam"}},
			expect{status: Killed, killers: familyKillers(all), covering: []string{"two:TestFam", "two:TestFamPart0", "two:TestFamPart1"}}},
		// A mutant of a file that is not Go (std's .ergo sources) is in no
		// block: the tests that read it (one:TestWord, its test log) and
		// those its row names judge it. A named test that fails on the
		// unmutated tree leaves it unjudged (GT8b).
		{&Mutant{Name: "data-named", File: "one/word.txt", From: "hi", To: "ho", Killers: []string{"one:TestWord"}},
			expect{status: Killed, killers: []string{"one:TestWord"}, covering: []string{"one:TestWord"}}},
		{&Mutant{Name: "data-wrong-killer", File: "one/word.txt", From: "hi", To: "ho", Killers: []string{"one:TestGreet"}},
			expect{status: Killed, killers: []string{"one:TestWord"}, covering: []string{"one:TestGreet", "one:TestWord"}}},
		{&Mutant{Name: "data-broken-killer", File: "one/word.txt", From: "hi", To: "ho", Killers: []string{"one:TestBroken", "one:TestWord"}},
			expect{status: Unjudged}},
		// A command killer runs in the mutated copy, as a test does.
		{&Mutant{Name: "data-command", File: "one/word.txt", From: "hi", To: "ho", Killers: []string{"command:word"}},
			expect{status: Killed, killers: []string{"command:word", "one:TestWord"}, covering: []string{"command:word", "one:TestWord"}}},
		{&Mutant{Name: "data-unnamed", File: "one/word.txt", From: "hi", To: "ho"},
			expect{status: Killed, killers: []string{"one:TestWord"}, covering: []string{"one:TestWord"}}},
		// Source mutants (Config.Sources): TestSrc fails on any of them, as
		// the examples runner's fmt and check legs do; only a value change
		// or a declared refusal is a kill.
		{&Mutant{Name: "src-value", File: "one/data.src", From: "value 1", To: "value 2", Anchor: srcAnchor}, srcExpect(Killed)},
		{&Mutant{Name: "src-unformatted", File: "one/data.src", From: "value 1", To: "value  1", Anchor: srcAnchor}, srcExpect(Invalid)},
		{&Mutant{Name: "src-refused", File: "one/data.src", From: "value 1", To: "value 1 ERROR", Anchor: srcAnchor}, srcExpect(Invalid)},
		// A generator's row (MUT-REGEN): the step writes two/names.go from
		// the mutated names.txt, which no longer declares Seven, so its
		// named killer's suite does not build: a kill. The same build
		// failure with a step that fails, or with a mutated source that
		// does not compile, is invalid (batch 23 fallout).
		{&Mutant{Name: "regen-breaks-build", File: "two/names.txt", From: "Seven", To: "Eight", Killers: []string{"two:TestNames"}, Regenerate: regenNames},
			expect{status: Killed, killers: []string{"two:TestNames"}, covering: []string{"two:TestNames"}, reason: "build", detail: "killed by build failure after regenerate: two/names_test.go:6:5: undefined: Seven"}},
		{&Mutant{Name: "regen-step-fails", File: "two/names.txt", From: "Seven", To: "Eight", Killers: []string{"two:TestNames"}, Regenerate: &Regenerate{Dir: "two", Argv: []string{"sh", "-c", "exit 3"}}},
			expect{status: Invalid, detail: "regenerate "}},
		{&Mutant{Name: "regen-source-does-not-compile", File: "gen/g.go", From: "return 1 }", To: "return \"x\" }", Killers: []string{"two:TestNames"}, Regenerate: regenNames},
			expect{status: Invalid, detail: "does not compile: gen/g.go:3:"}},
		// The same edit to the generated file itself, with no step: its
		// package compiles, its test does not build, and that is invalid.
		{&Mutant{Name: "names-renamed", File: "two/names.go", From: "Seven", To: "Eight", Killers: []string{"two:TestNames"}},
			expect{status: Invalid, detail: "build two: fail"}},
		// A generator's row that names no killer: its step breaks two's
		// build, which the coverage pass meets through two:TestReadsOne
		// (it reads one/f.go). A suite the row does not name stays
		// invalid, so a careless step cannot pass for a kill.
		{&Mutant{Name: "regen-unnamed-build", File: "one/f.go", From: `"bye"`, To: `"by"`, Regenerate: &Regenerate{Dir: "two", Argv: []string{"sh", "-c", `printf 'package two\n\nconst Eight = 7\n' > names.go`}}},
			expect{status: Invalid, covering: []string{"two:TestReadsOne"}, detail: "build two: fail"}},
		{&Mutant{Name: "src-refused-declared", File: "one/data.src", From: "value 1", To: "value 1 ERROR", Anchor: srcAnchor, Refused: "the refusal is the rule"}, srcExpect(Killed)},
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
	// two:TestReadsOne reads one/f.go, so it covers every mutant of that
	// file; it kills only a change of one's farewell (literal-bare and
	// literal-named, modeled above).
	for _, mu := range mutants {
		if w := want[mu.Name]; mu.File == "one/f.go" && w.status != Stale {
			want[mu.Name] = m.read(w, "two:TestReadsOne")
		}
	}

	// Seed 1 runs twice over one coverage store, each pass in a fresh work
	// directory: the second takes two's stored segment and measures one
	// again (one's TestBroken fails on the unmutated tree, so its segment
	// is never stored), and its verdicts are the model's too.
	base := pass{seed: seed, all: all, repo: repo, memory: memoryLimit, mutants: mutants, want: want}
	if seed == 1 {
		base.store = t.TempDir()
		for n := 1; n <= 2; n++ {
			var log strings.Builder
			base.check(t, &log)
			if n == 2 && !strings.Contains(log.String(), "coverage: 1 of 2 suites' segments reused") {
				t.Errorf("seed %d: the second pass over the store did not reuse two's segment alone:\n%s", seed, log.String())
			}
		}
		// Rows their named tests kill need no coverage at all (phase A).
		named := base
		named.sel = regexp.MustCompile(`^(literal-named|data-named)$`)
		var log strings.Builder
		named.check(t, &log)
		if !strings.Contains(log.String(), "coverage: not measured") || strings.Contains(log.String(), "coverage receipt") {
			t.Errorf("seed %d: a pass whose named tests kill every row measured coverage:\n%s", seed, log.String())
		}
		return
	}
	// A run stopped after a few verdicts (as a server restart stops one)
	// leaves them in its journal; the next run of the tree takes them,
	// judges only the rest, and removes the journal when it is complete.
	base.journal = t.TempDir()
	first := base
	first.cancelAfter = 4
	judged := map[string]bool{}
	for _, r := range first.check(t, io.Discard) {
		if r.Status != "" {
			judged[r.Name] = true
		}
	}
	if len(judged) < first.cancelAfter {
		t.Fatalf("seed %d: the stopped pass judged %d rows, want at least %d", seed, len(judged), first.cancelAfter)
	}
	second := base
	second.resumed = judged
	var log strings.Builder
	second.check(t, &log)
	if !strings.Contains(log.String(), fmt.Sprintf("resume: %d of %d rows", len(judged), len(mutants))) {
		t.Errorf("seed %d: the second pass did not resume the %d rows the first judged:\n%s", seed, len(judged), log.String())
	}
	if left, _ := os.ReadDir(base.journal); len(left) != 0 {
		t.Errorf("seed %d: a complete run left its journal: %v", seed, left)
	}
}

// pass is one run of the runner over the fake module: its coverage store
// and journal, the rows it selects, and, for a run stopped early, the
// number of verdicts after which it stops; resumed are the rows an
// earlier stopped run judged, which this one must not run again.
type pass struct {
	seed                 uint64
	all                  bool
	repo, store, journal string
	memory               uint64
	mutants              []*Mutant
	want                 map[string]expect
	sel                  *regexp.Regexp
	cancelAfter          int
	resumed              map[string]bool
}

// check runs the runner once over the repo and checks every verdict with
// the model's.
func (p pass) check(t *testing.T, log io.Writer) []Result {
	t.Helper()
	seed, all, repo, store, memoryLimit, mutants, want := p.seed, p.all, p.repo, p.store, p.memory, p.mutants, p.want
	logPath := filepath.Join(t.TempDir(), "ran.log")
	t.Setenv("FAKE_LOG", logPath)
	// go test put the toolchain first on this process's PATH; the runner
	// must put it there itself, as it does when run by go run.
	if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
		bin := filepath.Join(strings.TrimSpace(string(out)), "bin")
		kept := slices.DeleteFunc(filepath.SplitList(os.Getenv("PATH")), func(d string) bool { return d == bin })
		// Only where another go is on PATH (the host's); else the runner
		// could not start go at all.
		if slices.ContainsFunc(kept, func(d string) bool { _, err := os.Stat(filepath.Join(d, "go")); return err == nil }) {
			t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
		}
	}
	// An inherited GOROOT of no toolchain: the runner drops it for every
	// go it starts, or each fails "cannot find GOROOT" (gtb-goroot-stale).
	t.Setenv("GOROOT", "/nonexistent/loop-probe-x")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{
		Root:  repo,
		Work:  filepath.Join(t.TempDir(), "work"),
		Cover: []string{"./one", "./two"},
		Suites: []Suite{{Name: "one", Package: "./one", Env: []string{"FAKE_SUITE=one"}}, {Name: "two", Package: "./two", Env: []string{"FAKE_SUITE=two"}, Families: []string{"TestFam"}},
			// A killer that is not a go test (Ledger.Commands).
			{Name: commandSuite, Commands: map[string][]string{"word": {"grep", "-qx", "hi", "{root}/one/word.txt"}}, Only: []string{}, Named: []string{"word"}}},
		Workers:    2,
		Procs:      1,
		Tests:      2,
		Memory:     memoryLimit,
		All:        all,
		CoverStore: store,
		JournalDir: p.journal,
		// The floor stops TestSpin's block (spins): the fake tests take
		// milliseconds, so it never stops one that would end.
		TestFloor: 20 * time.Second,
		Sources: []SourceCheck{{
			Ext:    ".src",
			Tool:   Tool{Name: "src", Package: "./cmd/src"},
			Format: []string{"{bin:src}", "fmt", "{file}"},
			Check:  []string{"{bin:src}", "check", "{dir}"},
		}},
		Log: log, // the engine's logf writes under its lock
	}
	if p.cancelAfter > 0 {
		cfg.Progress = func(results []Result) {
			if len(slices.DeleteFunc(results, func(r Result) bool { return r.Status == "" })) >= p.cancelAfter {
				cancel()
			}
		}
	}
	results, cov, err := Run(ctx, cfg, []Job{{Path: filepath.Join(repo, "one/testdata/mutants.json"), Ledger: &Ledger{Mutants: mutants}, Select: p.sel}})
	for _, ct := range cov.Tests {
		if ct.Failed && ct.ID != "one:TestBroken" {
			// Only TestBroken fails on the unmutated tree by design.
			t.Errorf("seed %d: %s fails on the unmutated tree:\n%s", seed, ct.ID, ct.Detail)
		}
	}
	if err != nil && ctx.Err() == nil {
		t.Fatalf("seed %d: run: %v", seed, err)
	}
	byName := map[string]*Mutant{}
	selected := 0
	for _, mu := range mutants {
		byName[mu.Name] = mu
		if p.sel == nil || p.sel.MatchString(mu.Name) {
			selected++
		}
	}
	if len(results) != selected {
		t.Fatalf("seed %d: %d results for %d mutants", seed, len(results), selected)
	}
	ran := map[string][]string{}
	logText, _ := os.ReadFile(logPath)
	for _, line := range strings.Split(strings.TrimSpace(string(logText)), "\n") {
		if mutant, test, ok := strings.Cut(line, "\t"); ok {
			ran[mutant] = append(ran[mutant], test)
		}
	}
	for i, res := range results {
		if res.Status == "" && p.cancelAfter > 0 {
			continue // not judged before the stop
		}
		mu := byName[res.Name]
		if mu == nil {
			t.Fatalf("seed %d: result %d names no mutant: %+v", seed, i, res)
		}
		w := want[mu.Name]
		bad := func(format string, args ...any) {
			t.Errorf("seed %d (replay: %s=%d), mutant %s: %s\n  model: %+v\n  runner: status %s killer %s (%s) killers %v ran %v\n  %s", seed, seedVariable, seed, mu.Name,
				fmt.Sprintf(format, args...), w, res.Status, res.Killer, res.Reason, res.Killers, res.Ran, firstLine(res.Detail))
		}
		if p.resumed[mu.Name] && len(ran[mu.Name]) > 0 {
			bad("an earlier run judged it, and this one ran %v on it again", ran[mu.Name])
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
		if w.detail != "" && !strings.HasPrefix(res.Detail, w.detail) {
			bad("detail %q, want %q first", res.Detail, w.detail)
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
	return results
}

// familyKillers are family-named's killers: the family alone, which
// kills in the named pass; with -all the covering part too.
func familyKillers(all bool) []string {
	if all {
		return []string{"two:TestFam", "two:TestFamPart1"}
	}
	return []string{"two:TestFam"}
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

// TestCopyStartsNoMaintenance: the runner's copy is a Git repository whose
// own configuration turns automatic gc and maintenance off, so no git a
// test runs in it starts a repack (wf10, 2026-11-19: repacks of the
// scratch repositories ran 13 and 40 minutes on a nearly full disk). The
// configuration is read from the copy's file, not through git's
// environment, which the runner also sets. Mutant:
// mutants-copy-maintenance-on.
func TestCopyStartsNoMaintenance(t *testing.T) {
	dst := t.TempDir()
	writeFile(t, filepath.Join(dst, "f.go"), "package f\n")
	if err := commitCopy(dst, []treeFile{{path: "f.go", mode: 0o644, data: []byte("package f\n")}}); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dst, ".git", "config")
	for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false"} {
		if got := git(t, dst, "config", "--file", config, "--get", key); got != want {
			t.Errorf("the copy's %s is %q, want %q", key, got, want)
		}
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
