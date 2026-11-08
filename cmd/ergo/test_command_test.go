package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testRunCLI runs the CLI in directory with its emission cache under xdg
// and its temporary files under tmp, and returns stdout, stderr and the
// exit code.
func testRunCLI(t *testing.T, binary, directory, xdg, tmp string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg, "TMPDIR="+tmp)
	stdout, stderr, code := runSmokeCommand(t, processDeadline, command)
	return string(stdout), string(stderr), code
}

// emptyDir fails the test when dir holds anything.
func emptyDir(t *testing.T, dir, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s holds %v after ergo test", what, names)
	}
}

// TestTestCommand drives `ergo test` as a process over each outcome of
// the runner and each refusal of the command. The exit status is the
// runner's: 0 for a pass, 1 for a failed test, 2 for a defect. A usage
// error is also 2, and it prints no report line and runs nothing. A source
// that does not check prints its check envelope and no PASS line. Test mode
// never calls main. The test binary leaves nothing in the working
// directory or the temporary directory.
func TestTestCommand(t *testing.T) {
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := filepath.Join(t.TempDir(), "xdg")
	tmp := t.TempDir()
	cwd := t.TempDir()
	write := func(name, text string) string { return smokeWrite(t, root, name, text) }
	run := func(args ...string) (string, string, int) {
		t.Helper()
		stdout, stderr, code := testRunCLI(t, binary, cwd, xdg, tmp, args...)
		emptyDir(t, cwd, "the working directory")
		emptyDir(t, tmp, "the temporary directory")
		return stdout, stderr, code
	}
	main := "func main() needs Console { Console.Println(\"MAIN-MUST-NOT-RUN\") }\n\n"
	pass := write("pass.ergo", main+"func double(n int) int { n * 2 }\n\ntest Doubles { assert double(2) == 4 }\n\ntest Halves(n int) { assert double(n) - n == n }\n")
	if stdout, stderr, code := run("test", pass); code != 0 || stdout != "ok Doubles\nok Halves (100 cases)\nPASS (2 tests)\n" {
		t.Fatalf("a passing file: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	fail := write("fail.ergo", main+"test First { assert 1 == 2 }\n\ntest Second { assert true }\n")
	stdout, stderr, code := run("test", fail)
	if code != 1 || !strings.HasPrefix(stdout, "FAIL First: assert failed at fail.ergo:3:14: 1 == 2\nok Second\nFAIL (1 of 2 tests)\n") || strings.Contains(stdout, "PASS") {
		t.Fatalf("a failed assert: exit %d stdout %q stderr %q; want exit 1, the later test run, no PASS", code, stdout, stderr)
	}
	defect := write("defect.ergo", "func at(xs []int, i int) int { xs[i] }\n\ntest Before { assert true }\n\ntest Panics { assert at([]int{}, 1) == 0 }\n\ntest After { assert true }\n")
	stdout, stderr, code = run("test", defect)
	if code != 2 || stdout != "ok Before\nFAIL Panics: a defect, which stops the run\n" || !strings.Contains(stderr, "panic: ") || !strings.Contains(stderr, "defect.ergo:1") {
		t.Fatalf("a defect: exit %d stdout %q stderr %q; want exit 2, the run stopped, the runtime's report", code, stdout, stderr)
	}
	for _, main := range []string{stdout, stderr} {
		if strings.Contains(main, "MAIN-MUST-NOT-RUN") {
			t.Fatal("test mode ran main")
		}
	}
	// --seed N and --seed=N reach the generator: the report names the seed
	// and both spellings replay it byte for byte.
	replay := write("replay.ergo", "test Residue(value int) { assert value % 7 != 3 }\n")
	seeded, _, code := run("test", replay, "--seed", "7")
	again, _, code2 := run("test", replay, "--seed=7")
	if code != 1 || code2 != 1 || !regexp.MustCompile(`(?m)^FAIL Residue \(case \d+ of 100, seed 7: value = -?\d+\)`).MatchString(seeded) || seeded != again {
		t.Fatalf("--seed 7: exit %d %q; --seed=7: exit %d %q", code, seeded, code2, again)
	}
	// Usage errors: exit 2, nothing on stdout, nothing run.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"test", replay, "--seed", "x"}, "--seed takes a decimal number"},
		{[]string{"test", replay, "--seed=-1"}, "--seed takes a decimal number"},
		{[]string{"test", replay, "--seed"}, "--seed requires a value"},
		{[]string{"test", replay, "-o", "out"}, "-o is only supported by build"},
		{[]string{"test", replay, "--strict"}, "--strict is only supported by diagnostics"},
		{[]string{"test", replay, replay}, "incorrect arguments for test"},
		{[]string{"test", filepath.Join(root, "replay.txt")}, "source file must have .ergo extension"},
		{[]string{"test"}, "source file required"},
	} {
		stdout, stderr, code := run(c.args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, c.want) {
			t.Fatalf("ergo %v: exit %d stdout %q stderr %q; want exit 2 and %q", c.args, code, stdout, stderr, c.want)
		}
	}
	for _, command := range []string{"build", "run", "check"} {
		if _, stderr, code := run(command, "--seed", "1", replay); code == 0 || !strings.Contains(stderr, "--seed is only supported by test") {
			t.Fatalf("ergo %s --seed: exit %d stderr %q", command, code, stderr)
		}
	}
	// A source that does not check: its findings as text on stderr (the
	// envelope with --json), exit 1, no PASS line.
	invalid := write("invalid.ergo", "test Bad { assert 1 }\n")
	stdout, stderr, code = run("test", invalid)
	if code != 1 || stdout != "" || !regexp.MustCompile(`(?m)^\S*invalid\.ergo:1:\d+: error S108: .+\ncheck failed\n\z`).MatchString(stderr) {
		t.Fatalf("an invalid file: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	stdout, stderr, code = run("test", "--json", invalid)
	if code != 1 || !strings.Contains(stdout, `"S108"`) || strings.Contains(stdout, "PASS") || stderr != "check failed\n" {
		t.Fatalf("an invalid file with --json: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

// TestBuildAndTestActionsDiffer: `ergo build -work` and `ergo test -work`
// of one source name two modules of the emission cache. The build module
// holds no Go from the test body; the test module maps it. An unchanged
// second `ergo test -work` reuses its module without rewriting a file, and
// the test binary is never written into the module.
func TestBuildAndTestActionsDiffer(t *testing.T) {
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := filepath.Join(t.TempDir(), "xdg")
	tmp := t.TempDir()
	source := smokeWrite(t, root, "main.ergo", "func main() needs Console { Console.Println(\"main\") }\n\ntest OnlyTest {\n\tassert 2 + 2 == 4\n}\n")
	stdout, stderr, code := testRunCLI(t, binary, root, xdg, tmp, "build", "main.ergo", "-o", "app", "-work")
	if code != 0 || stdout != "app\n" {
		t.Fatalf("build -work: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	built := workDir(t, stderr)
	stdout, stderr, code = testRunCLI(t, binary, root, xdg, tmp, "test", "main.ergo", "-work")
	if code != 0 || stdout != "ok OnlyTest\nPASS (1 tests)\n" {
		t.Fatalf("test -work: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	tested := workDir(t, stderr)
	modules := filepath.Join(xdg, "ergo", "modules")
	if built == tested || filepath.Dir(built) != modules || filepath.Dir(tested) != modules {
		t.Fatalf("build module %s and test module %s, want two modules of %s", built, tested, modules)
	}
	body := regexp.MustCompile(`(?m)^\s*//line ` + regexp.QuoteMeta(source) + `:4\b`)
	read := func(dir string) string {
		data, err := os.ReadFile(filepath.Join(dir, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if body.MatchString(read(built)) || !body.MatchString(read(tested)) {
		t.Fatalf("the test body's line 4: in the build module %v, in the test module %v; want only the test module", body.MatchString(read(built)), body.MatchString(read(tested)))
	}
	before := moduleState(t, tested)
	stdout, stderr, code = testRunCLI(t, binary, root, xdg, tmp, "test", "main.ergo", "-work")
	if code != 0 || workDir(t, stderr) != tested || stdout != "ok OnlyTest\nPASS (1 tests)\n" {
		t.Fatalf("second test -work: exit %d stdout %q stderr %q, want the module %s", code, stdout, stderr, tested)
	}
	after := moduleState(t, tested)
	if len(after) != len(before) {
		t.Fatalf("the second test run changed the module's files: %d, then %d", len(before), len(after))
	}
	for p, s := range before {
		a, ok := after[p]
		if !ok || a.digest != s.digest || !os.SameFile(a.info, s.info) || !a.info.ModTime().Equal(s.info.ModTime()) {
			t.Fatalf("the second test run rewrote %s", p)
		}
		if a.info.Mode()&0o111 != 0 {
			t.Fatalf("the module holds the executable %s", p)
		}
	}
	out, err := exec.Command(filepath.Join(root, "app")).Output()
	if err != nil || string(out) != "main\n" {
		t.Fatalf("the build binary printed %q (%v), want main only", out, err)
	}
	emptyDir(t, tmp, "the temporary directory")
	if entries, _ := os.ReadDir(root); len(entries) != 2 {
		t.Fatalf("the source directory holds %d entries, want main.ergo and app", len(entries))
	}
}
