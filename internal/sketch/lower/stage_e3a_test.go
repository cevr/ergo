package lower

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage E3a (196l; ledger row C20): a test is lowered only by the test
// build, LowerTests; the run build, Lower, contains no Go from a test. Each
// program of testdata/e3 states the stdout of ergo run (`// run> `) and of
// its test binary (`// test> `, `// test exit N`); the sweep runs both.

// e3Program is one program of testdata/e3 and its expected outputs.
type e3Program struct {
	name, src string
	run       []string // ergo run's stdout lines
	test      []string // the test binary's stdout lines
	exit      int      // the test binary's exit status
}

func e3Programs(t *testing.T) []e3Program {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "e3", "*.ergo"))
	if err != nil || len(paths) < 12 {
		t.Fatalf("testdata/e3 holds %d programs (%v); the sweep needs 12 or more", len(paths), err)
	}
	var out []e3Program
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		prog := e3Program{name: "e3_" + strings.TrimSuffix(filepath.Base(p), ".ergo"), src: string(data), exit: -1}
		for _, line := range strings.Split(prog.src, "\n") {
			switch {
			case strings.HasPrefix(line, "// run> "):
				prog.run = append(prog.run, strings.TrimPrefix(line, "// run> "))
			case strings.HasPrefix(line, "// test> "):
				prog.test = append(prog.test, strings.TrimPrefix(line, "// test> "))
			case strings.HasPrefix(line, "// test exit "):
				prog.exit, err = strconv.Atoi(strings.TrimPrefix(line, "// test exit "))
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
			}
		}
		if len(prog.run) == 0 || len(prog.test) == 0 || prog.exit < 0 {
			t.Fatalf("%s states no run output, test output or test exit", p)
		}
		out = append(out, prog)
	}
	return out
}

// lowerTestBuild checks src and lowers its test build.
func lowerTestBuild(t *testing.T, name, src string) *lowered {
	t.Helper()
	out := &lowered{name: name, src: src}
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out.file = file
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, nil)
	if len(out.diags) > 0 {
		t.Fatalf("%s: %v", name, out.diags)
	}
	u := Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	files, err := LowerTests(u)
	if err != nil {
		t.Fatalf("%s: the test build: %v\n%s", name, err, src)
	}
	out.files = files
	return out
}

// runExeArgs runs a built program with args and returns its stdout, stderr
// and exit code.
func runExeArgs(t *testing.T, exe string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd)
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func lines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// TestE3Sweep builds the run build and the test build of every program of
// testdata/e3 and runs both: ergo run prints main's output only, and the
// test binary prints each test's verdict and exits with the run's status.
func TestE3Sweep(t *testing.T) {
	progs := e3Programs(t)
	var builds []*lowered
	for _, p := range progs {
		builds = append(builds, mustLower(t, p.name+"_run", p.src), lowerTestBuild(t, p.name+"_test", p.src))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name+"_run"])
		if code != 0 || strings.Join(lines(stdout), "\n") != strings.Join(p.run, "\n") {
			t.Errorf("%s: ergo run: exit %d, stdout %q, stderr %q; want %q", p.name, code, stdout, firstLines(stderr, 8), p.run)
		}
		stdout, stderr, code = runExe(t, exes[p.name+"_test"])
		if code != p.exit || strings.Join(lines(stdout), "\n") != strings.Join(p.test, "\n") {
			t.Errorf("%s: test binary: exit %d (want %d), stdout:\n%s\nwant:\n%s\nstderr:\n%s", p.name, code, p.exit, stdout, strings.Join(p.test, "\n"), firstLines(stderr, 12))
		}
		// A run that fails names its count on standard error; a defect
		// is the runtime's own report.
		switch p.exit {
		case 1:
			if !strings.Contains(stderr, "tests failed") {
				t.Errorf("%s: a failed run's stderr names no count: %q", p.name, stderr)
			}
		case 2:
			if !strings.Contains(stderr, "panic: ") {
				t.Errorf("%s: a defect's stderr holds no runtime report: %q", p.name, stderr)
			}
		}
	}
}

// TestPropertySeed: --seed N and --seed=N replay the same cases, another
// seed draws others, and any other argument is a usage error.
func TestPropertySeed(t *testing.T) {
	src := `func clamp(n int) int {
	if n > 100 {
		return 100
	}
	n
}

test Clamped(n int, xs []int) {
	assert clamp(n) < 50 || len(xs) > 3
}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "seed", src)}, true)
	if exes == nil {
		return
	}
	exe := exes["seed"]
	one, _, code := runExeArgs(t, exe)
	again, _, _ := runExeArgs(t, exe, "--seed", "1")
	eq, _, _ := runExeArgs(t, exe, "--seed=1")
	if code != 1 || one != again || one != eq {
		t.Errorf("the default seed is 1 and replays: exit %d\n%s\n%s\n%s", code, one, again, eq)
	}
	if !regexp.MustCompile(`^FAIL Clamped \(case \d+ of 100, seed 1: n = -?\d+, xs = \[\]int\{(-?\d+(, -?\d+)*)?\}\): assert failed at main\.ergo:9:2: clamp\(n\) < 50 \|\| len\(xs\) > 3\n`).MatchString(one) {
		t.Errorf("the FAIL line names the case, the seed, the arguments and the assert:\n%s", one)
	}
	two, _, _ := runExeArgs(t, exe, "--seed", "2")
	if !strings.Contains(two, "seed 2:") || two == one {
		t.Errorf("seed 2 draws its own cases:\n%s", two)
	}
	_, stderr, code := runExeArgs(t, exe, "--verbose")
	if code != 2 || !strings.Contains(stderr, "usage: [--seed N]") {
		t.Errorf("an unknown argument: exit %d, stderr %q", code, stderr)
	}
}

// directiveRe matches the position of a //line or /*line*/ directive.
var directiveRe = regexp.MustCompile(`(?://|/\*)line [^\s*]*:(\d+):(\d+)`)

// testSpans are the line and column ranges of the test declarations of f.
func testSpans(f *syntax.File) [][2][2]int {
	var spans [][2][2]int
	for _, d := range f.Decls {
		if td, ok := d.(*syntax.TestDecl); ok {
			l0, c0 := f.Position(td.Pos())
			l1, c1 := f.Position(td.End())
			spans = append(spans, [2][2]int{{l0, c0}, {l1, c1}})
		}
	}
	return spans
}

func before(a, b [2]int) bool { return a[0] < b[0] || a[0] == b[0] && a[1] < b[1] }

// blankTests replaces every test declaration of src with spaces, keeping
// its line feeds, so every other position stays where it was.
func blankTests(src string, f *syntax.File) string { return blankDecls(src, f, false) }

// blankDecls replaces every test declaration of src, and every import
// declaration when imports is set, with spaces, keeping its line feeds, so
// every other position stays where it was.
func blankDecls(src string, f *syntax.File, imports bool) string {
	b := []byte(src)
	for _, d := range f.Decls {
		_, test := d.(*syntax.TestDecl)
		_, imp := d.(*syntax.ImportDecl)
		if test || imports && imp {
			for i := int(d.Pos()); i < int(d.End()); i++ {
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		}
	}
	return string(b)
}

// TestRunBuildOmitsTests proves rule 1 of Test mode on the emitted Go of
// every sweep program (runBuildOmitsTests); the generated programs of
// TestTestFactsStayInTests prove it too.
func TestRunBuildOmitsTests(t *testing.T) {
	for _, p := range e3Programs(t) {
		runBuildOmitsTests(t, p.name, p.src, false)
	}
}

// runBuildOmitsTests checks rule 1 of Test mode on the run build of src,
// which must declare a test, and returns it: no Go line of the run build
// maps into a test declaration, no test-build entity appears, and the run
// build is the run build of the same source with its tests blanked out
// (and its imports, when blankImports is set: a library only a test uses
// is then no part of the run build either).
func runBuildOmitsTests(t *testing.T, name, src string, blankImports bool) *lowered {
	t.Helper()
	run := mustLower(t, name, src)
	spans := testSpans(run.file)
	if len(spans) == 0 {
		t.Fatalf("%s declares no test", name)
	}
	code := run.mainGo()
	for _, m := range directiveRe.FindAllStringSubmatch(code, -1) {
		line, _ := strconv.Atoi(m[1])
		col, _ := strconv.Atoi(m[2])
		for _, s := range spans {
			if at := [2]int{line, col}; !before(at, s[0]) && before(at, s[1]) {
				t.Errorf("%s: the run build has Go from the test at %d:%d (%s)", name, s[0][0], s[0][1], m[0])
			}
		}
	}
	for _, h := range testHelpers {
		if strings.HasPrefix(h.base, "ergo") && strings.Contains(code, h.base) {
			t.Errorf("%s: the run build names the test build's %s", name, h.base)
		}
	}
	if strings.Contains(code, "ergoTest") || strings.Contains(code, "ergoGen") {
		t.Errorf("%s: the run build names a test function or a generator", name)
	}
	blank := mustLower(t, name+"_blank", blankDecls(src, run.file, blankImports))
	if blank.mainGo() != code {
		t.Errorf("%s: the run build differs from the run build of the source without its tests:\n%s", name, goDiff(blank.mainGo(), code))
	}
	return run
}

// goDiff names the lines only one of two Go files has, at most 20.
func goDiff(without, with string) string {
	count := map[string]int{}
	for _, l := range strings.Split(without, "\n") {
		count[l]++
	}
	var b strings.Builder
	n := 0
	for _, l := range strings.Split(with, "\n") {
		if count[l] > 0 {
			count[l]--
			continue
		}
		if n++; n <= 20 {
			b.WriteString("+ " + l + "\n")
		}
	}
	for _, l := range strings.Split(without, "\n") {
		if count[l] > 0 {
			count[l]--
			if n++; n <= 20 {
				b.WriteString("- " + l + "\n")
			}
		}
	}
	return b.String()
}

// TestTestBuildHasEveryTest: the test build declares one Go function per
// test, and the run build's Go main is replaced by the runner.
func TestTestBuildHasEveryTest(t *testing.T) {
	for _, p := range e3Programs(t) {
		tb := lowerTestBuild(t, p.name, p.src)
		code := tb.mainGo()
		for _, test := range tb.pkg.Tests {
			if !regexp.MustCompile(`(?m)^func ergoTest` + test.Name + `\(`).MatchString(code) {
				t.Errorf("%s: the test build has no Go function for test %s", p.name, test.Name)
			}
		}
		if !strings.Contains(code, "ergoTests(") || strings.Contains(code, "hostConsole{}") {
			t.Errorf("%s: the test build's main is not the test runner", p.name)
		}
	}
}
