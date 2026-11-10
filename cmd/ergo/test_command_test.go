package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	stdout, stderr, code := runSmokeCommand(t, command)
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
	if code != 1 || code2 != 1 || !regexp.MustCompile(`(?m)^FAIL Residue \(case \d+ of 100, seed 7, size \d+: value = -?\d+[;)]`).MatchString(seeded) || seeded != again {
		t.Fatalf("--seed 7: exit %d %q; --seed=7: exit %d %q", code, seeded, code2, again)
	}
	// The FAIL line names the command that replays the case (L10).
	if !strings.Contains(seeded, "; replay: ergo test "+replay+" --seed 7): ") {
		t.Fatalf("--seed 7 prints no replay command: %q", seeded)
	}
	testReceipt(t, write, run)
	// Each replay of a shrink builds its own world (fstest.NewMemory, the
	// builder of fs.Files and fs.Writes): the property fails only on a
	// fresh file system, so it shrinks only if no replay sees an earlier
	// replay's file.
	fresh := write("fresh.ergo", "import fs\nimport fstest\n\ntype Disk { files fs.Files, w fs.Writes }\n\n"+
		"test FreshWorld(d Disk, n int) {\n\tlet names = try d.files.ReadDir(\".\")\n\ttry d.w.WriteFile(\"a.txt\", \"x\")\n\tassert n < 1000 || len(names) > 0\n}\n\n"+
		"test Uses {\n\tlet _ = fstest.NewMemory()\n}\n")
	if stdout, stderr, code := run("test", fresh); code != 1 || !strings.Contains(stdout, "; shrunk from: d = ") {
		t.Fatalf("a property over a built world did not shrink: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	// A refuted law prints the command that reruns it alone (X1b): ergo
	// test on the source as it was given, --law and the seed. That command
	// prints the same FAIL line, and runs no test.
	laws := write("replaylaw.ergo", "law Residue(value int) {\n\tvalue % 7 != 3\n}\n\ntest Other { assert true }\n")
	stdout, _, code = run("test", laws, "--seed", "7")
	rerun, _, code2 := run("test", laws, "--law", "Residue", "--seed", "7")
	refuted, _, _ := strings.Cut(rerun, "\n")
	want := refuted + "\n\treplay: ergo test " + laws + " --law Residue --seed 7\n"
	if code != 1 || code2 != 1 || !strings.HasPrefix(refuted, "FAIL law Residue (case ") || !strings.Contains(stdout, "ok Other\n"+want) || rerun != want+"FAIL (1 of 1 tests)\n" {
		t.Fatalf("a refuted law: exit %d %q; its replay: exit %d %q", code, stdout, code2, rerun)
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

// testReceipt: `ergo test FILE --receipt PATH` writes the law receipt, a
// JSON object whose fields are exactly the schema's (runtime.md Tests 8):
// a listed law that holds is proved and a drawn one tested, each with its
// case count, and a refuted law carries its shrunk counterexample, which
// holds a quoted string here, so the receipt's escaping is read back too. run checks that nothing else is
// written.
func testReceipt(t *testing.T, write func(name, text string) string, run func(args ...string) (string, string, int)) {
	t.Helper()
	// A machine's reachability law is a law run too, so the receipt has
	// its entry: proved, with the search's report.
	laws := write("laws.ergo", "law Twice(b bool) { !!b == b }\n\nlaw Holds(n int) { n == n }\n\nlaw Short(s string) { len(s) < 2 }\n\n"+
		"type Light = Red{} | Green{}\n\ntype Tick = Flip{}\n\n"+
		"func flip(l Light, _ Tick) Decision[Light, int] {\n\tmatch l {\n\tcase Red:\n\t\tEnter[Light]{Next: Green}\n\tcase Green:\n\t\tEnter[Light]{Next: Red}\n\t}\n}\n\n"+
		"type Lamp machine[Light, Tick] {\n\tinitial: Red,\n\tstep:    flip,\n\n\tlaw Lit reaches Green,\n}\n")
	path := filepath.Join(t.TempDir(), "receipt.json")
	stdout, stderr, code := run("test", laws, "--receipt", path)
	data, err := os.ReadFile(path)
	if code != 1 || err != nil {
		t.Fatalf("--receipt: exit %d stdout %q stderr %q, read: %v", code, stdout, stderr, err)
	}
	type lawRun struct {
		Run            string `json:"run"`
		Status         string `json:"status"`
		Cases          int    `json:"cases"`
		Counterexample string `json:"counterexample"`
		Why            string `json:"why"`
		Search         string `json:"search"`
	}
	var got struct {
		Schema   int      `json:"schema"`
		Source   string   `json:"source"`
		Revision string   `json:"revision"`
		Seed     uint64   `json:"seed"`
		Target   string   `json:"target"`
		Laws     []lawRun `json:"laws"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("the receipt does not read back: %v\n%s", err, data)
	}
	if len(got.Laws) != 4 {
		t.Fatalf("receipt %s: want 4 law runs\nstdout %q", data, stdout)
	}
	// The machine's law runs in its machine's place in the report; the
	// top-level laws keep their order.
	var lit lawRun
	for i, l := range got.Laws {
		if l.Run == "law Lamp.Lit" {
			lit = l
			got.Laws = append(got.Laws[:i:i], got.Laws[i+1:]...)
			break
		}
	}
	short := regexp.MustCompile(`^case (\d+) of 100, seed 1, size \d+: s = "[^"]{2}"$`).FindStringSubmatch(got.Laws[len(got.Laws)-1].Counterexample)
	if got.Schema != 1 || got.Source != laws || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got.Revision) || got.Seed != 1 || got.Target != "go" ||
		len(got.Laws) != 3 || got.Laws[0] != (lawRun{Run: "law Twice", Status: "proved", Cases: 2}) || got.Laws[1] != (lawRun{Run: "law Holds", Status: "tested", Cases: 100}) ||
		got.Laws[2].Run != "law Short" || got.Laws[2].Status != "refuted" || short == nil || strconv.Itoa(got.Laws[2].Cases) != short[1] ||
		!regexp.MustCompile(regexp.QuoteMeta("FAIL law Short ("+got.Laws[2].Counterexample)+`[;)]`).MatchString(stdout) ||
		lit.Status != "proved" || !strings.HasPrefix(lit.Search, "proved by the run ") || !strings.Contains(stdout, "ok law Lamp.Lit ("+lit.Search+")") {
		t.Fatalf("receipt %s\nstdout %q", data, stdout)
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
