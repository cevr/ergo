package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
)

// gitIn runs git in a fixture repository.
func gitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=gate", "-c", "user.email=gate@invalid", "-c", "commit.gpgsign=false"}, args...)...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

// wholeFixture is a gate over a fixture checkout with the whole-gate key on,
// keying only sh among the host programs: git's fingerprint hashes its exec
// path, about 0.25 s a collection on this host (measured), and each run
// collects twice. TestWholeGateKeyCollector and the tools tests cover the
// fingerprints.
func wholeFixture(t *testing.T, root string, steps []*Step) *gate {
	t.Helper()
	state, err := checkoutStateOf(root)
	if err != nil {
		t.Fatal(err)
	}
	return &gate{root: root, cacheDir: t.TempDir(), steps: steps, programs: []string{"sh"},
		whole: &wholeGate{mechanism: "rss: test", identity: state}}
}

// outsideCount is a step that counts its runs in a file outside the
// checkout, so a run leaves the checkout as it found it.
func outsideCount(counter string) []string {
	return []string{"sh", "-c", `echo run >> "$1" && echo "counted"`, "sh", counter}
}

func countIn(t *testing.T, counter string) int {
	t.Helper()
	content, err := os.ReadFile(counter)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(content), "run\n")
}

func runWhole(t *testing.T, g *gate) (int, string) {
	t.Helper()
	var out bytes.Buffer
	run := g.derive(g.steps, g.noCache, g.whole)
	return run.run(&out), out.String()
}

// The whole gate records a passed run and replays it at the same key,
// starting no step; any keyed change runs it again; a failed run withdraws
// the record; a step that writes into the checkout keeps a run from
// recording.
func TestWholeGateRunRecordsAndReplays(t *testing.T) {
	root := fixture(t)
	outside := t.TempDir()
	counter := filepath.Join(outside, "count")
	flag := filepath.Join(outside, "flag")
	write(t, outside, "flag", "ok\n")
	steps := []*Step{
		{Name: "counted", Argv: outsideCount(counter)},
		{Name: "flagged", Argv: []string{"sh", "-c", `test -f "$1" && echo "flag present"`, "sh", flag}},
	}
	g := wholeFixture(t, root, steps)
	code, out := runWhole(t, g)
	if code != 0 || !strings.Contains(out, "gate: recorded whole-gate key ") {
		t.Fatalf("first run exit %d, no record:\n%s", code, out)
	}
	code, out = runWhole(t, g)
	wantAll(t, out, "== counted: ok, replayed (passed in ", "counted\n", "== flagged: ok, replayed (passed in ", "flag present\n", "gate: replayed 2 steps (key ")
	if code != 0 || countIn(t, counter) != 1 {
		t.Fatalf("second run exit %d, ran %d times; want a replay:\n%s", code, countIn(t, counter), out)
	}
	// The replay before the scope prints the identity line, then the record.
	file, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	code, replayed := replayWholeGate(root, "rss: test", file)
	_ = code
	file.Close()
	// replayWholeGate keys every declared host program, so it computes
	// another key than this fixture's gate: it must not replay.
	if replayed {
		t.Fatal("a replay under another set of host programs")
	}
	// A tracked edit, an ignored touch and a new empty directory each rerun.
	for i, change := range []func(){
		func() { write(t, root, "src/input.txt", "two\n") },
		func() { write(t, root, "runs/ignored.log", "x\n") },
		func() {
			if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		change()
		time.Sleep(10 * time.Millisecond)
		code, out = runWhole(t, g)
		if code != 0 || countIn(t, counter) != 2+i {
			t.Fatalf("change %d: exit %d, ran %d times:\n%s", i, code, countIn(t, counter), out)
		}
	}
	// The ignored file was written within 2 s of the key: that run did not
	// record, so the same state runs again until the window passes.
	// A failed run at a recorded key withdraws the record.
	time.Sleep(replay.RecentWindow + 100*time.Millisecond)
	code, out = runWhole(t, g)
	if code != 0 || !strings.Contains(out, "gate: recorded whole-gate key ") {
		t.Fatalf("exit %d, no record after the window:\n%s", code, out)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	code, out = runWhole(t, g)
	if code != 0 || !strings.Contains(out, "replayed") {
		t.Fatalf("the input outside every key changed, and the inner tier did not replay:\n%s", out)
	}
	uncached := g.derive(steps, true, g.whole)
	var buffer bytes.Buffer
	if code := uncached.run(&buffer); code != 1 || !strings.Contains(buffer.String(), "gate: withdrew whole-gate record ") {
		t.Fatalf("--no-cache with a failing step exited %d and did not withdraw:\n%s", code, buffer.String())
	}
	code, out = runWhole(t, g)
	if code != 1 || strings.Contains(out, "replayed") {
		t.Fatalf("the withdrawn record replayed, or the run passed (exit %d):\n%s", code, out)
	}
	// A step that writes into the checkout changes the end key.
	write(t, outside, "flag", "ok\n")
	g.steps = append(slices.Clone(steps), &Step{Name: "writer", Argv: []string{"sh", "-c", `echo x > written.txt`}})
	code, out = runWhole(t, g)
	if code != 0 || !strings.Contains(out, "gate: no whole-gate record: the checkout or the host changed during the run: written.txt (added)") {
		t.Fatalf("exit %d; a run that wrote into the checkout recorded, or did not say why not:\n%s", code, out)
	}
}

// TestMergeTierUncached: in merge mode every step, the hermetic one too,
// runs its own command with -count=1 after the caller's GOFLAGS; no receipt
// or whole-gate record is looked up; and a verdict that differs from the
// record fails the merge tier and withdraws the record.
func TestMergeTierUncached(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	repository, _ := repositorySteps(t)
	// The durations file names neither a host nor a hermetic gate package:
	// declare the steps with one of each too, so every kind of go test step
	// is checked.
	durations, err := readDurations(repository)
	if err != nil {
		t.Fatal(err)
	}
	module := modulePath(repository)
	packages := append(slices.Sorted(maps.Keys(durations)), module+"/scripts/gate", module+"/scripts/gate/replay")
	steps, err := declareSteps(repository, packages)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go test ./scripts/gate (host, every run)", "go test (hermetic gate packages)", "go test (other packages)", "go test ./prototypes/sketch9/sketch (kernel)", "provenance analyzer"} {
		if !slices.ContainsFunc(steps, func(s *Step) bool { return s.Name == name }) {
			t.Fatalf("no step %q among the declared steps; the check is vacuous", name)
		}
	}
	// The merge tier runs each step's own command with the gate's uncached
	// default (stepEnvironment), the hermetic step too: GOFLAGS is the inner
	// tier's caller flags with -count=1 after them, so no go test the step
	// starts, at any depth, replays Go's test cache.
	hermeticSteps := 0
	for _, step := range steps {
		merged := uncachedStep(step)
		if merged.Name != step.Name || !slices.Equal(merged.Argv, step.Argv) || !slices.Equal(merged.Env, step.Env) {
			t.Errorf("the merge tier changed step %q to %q %q env %q", step.Name, merged.Name, merged.Argv, merged.Env)
		}
		if step.Hermetic {
			hermeticSteps++
		}
		before, _ := environmentVariable(stepCommandEnvironment(repository, step), "GOFLAGS")
		after, _ := environmentVariable(stepCommandEnvironment(repository, merged), "GOFLAGS")
		// The gate's -p bound follows the caller's flags in every step
		// (stepGOFLAGS), and the uncached default follows it.
		bounded := stepGOFLAGS("-mod=mod", stepCPUs())
		if after != bounded+" -count=1" {
			t.Errorf("merge step %q gets GOFLAGS %q, want the caller's -mod=mod and the -p bound with -count=1 after them (inner tier %q)", step.Name, after, before)
		}
		if want := map[bool]string{false: bounded + " -count=1", true: bounded}[step.Hermetic]; before != want {
			t.Errorf("inner step %q (hermetic %t) gets GOFLAGS %q, want %q", step.Name, step.Hermetic, before, want)
		}
	}
	if hermeticSteps == 0 {
		t.Fatal("no hermetic step among the declared steps; the merge tier's exception check is vacuous")
	}

	// A fixture: the inner tier records, the merge tier reruns every step
	// though a receipt and a record exist, and a verdict that differs fails
	// it and withdraws the record.
	root := fixture(t)
	outside := t.TempDir()
	flag := filepath.Join(outside, "flag")
	write(t, outside, "flag", "ok\n")
	fixtureSteps := []*Step{
		{Name: "cached", Argv: outsideCount(filepath.Join(outside, "cached")), Inputs: &Inputs{Paths: []string{"src/"}}},
		{Name: "counted", Argv: outsideCount(filepath.Join(outside, "counted"))},
		{Name: "flagged", Argv: []string{"sh", "-c", `test -f "$1"`, "sh", flag}},
	}
	// The steps read files outside the checkout, which the sandbox hides:
	// this checks the tier's verdict logic, on the host
	// (TestMergeTierSandbox* check the sandbox).
	g := wholeFixture(t, root, fixtureSteps)
	g.openSandbox = unsandboxed
	var out bytes.Buffer
	if code := g.mergeTier(&out); code != 0 {
		t.Fatalf("merge with no record exited %d:\n%s", code, out.String())
	}
	wantAll(t, out.String(), "; the inner tier runs first\n", "merge: the inner tier\n", "gate: recorded whole-gate key ", "merge: every step uncached\n",
		"merge: cached: inner pass, uncached pass\n", "merge: counted: inner pass, uncached pass\n", "merge: flagged: inner pass, uncached pass\n", "merge: verdicts equal (3 steps)\n")
	if countIn(t, filepath.Join(outside, "cached")) != 2 || countIn(t, filepath.Join(outside, "counted")) != 2 {
		t.Fatalf("the uncached run replayed a receipt or a record:\n%s", out.String())
	}
	out.Reset()
	if code := g.mergeTier(&out); code != 0 || strings.Contains(out.String(), "merge: the inner tier\n") {
		t.Fatalf("merge with a record exited %d or ran the inner tier:\n%s", code, out.String())
	}
	if countIn(t, filepath.Join(outside, "cached")) != 3 {
		t.Fatalf("the uncached run replayed the cached step's receipt:\n%s", out.String())
	}
	// The input outside every key changes: the inner tier still replays,
	// the merge tier does not.
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if code, replayedOut := runWhole(t, g); code != 0 || !strings.Contains(replayedOut, "gate: replayed 3 steps") {
		t.Fatalf("exit %d, no replay:\n%s", code, replayedOut)
	}
	out.Reset()
	code := g.mergeTier(&out)
	wantAll(t, out.String(), "merge: flagged: inner pass, uncached FAILED\n", "merge: FAILED: verdicts differ: flagged\n")
	if code != 1 {
		t.Fatalf("merge with a differing verdict exited %d:\n%s", code, out.String())
	}
	if code, after := runWhole(t, g); code != 1 || strings.Contains(after, "replayed") {
		t.Fatalf("the record the merge tier withdrew replayed, or the gate passed (exit %d):\n%s", code, after)
	}
}

// TestStepKeyFollowsLinks: a cached step whose input is a link keys what the
// link reaches. A link that leaves the checkout cannot be keyed: the step
// runs every time and says why. A link to a file in the checkout that the
// step does not select keys that file's content.
func TestStepKeyFollowsLinks(t *testing.T) {
	root := fixture(t)
	outside := t.TempDir()
	write(t, outside, "data.json", "{}\n")
	if err := os.Symlink(filepath.Join(outside, "data.json"), filepath.Join(root, "src", "data.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../docs/notes.md", filepath.Join(root, "lib", "notes.md")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-qm", "links")
	steps := []*Step{
		{Name: "outside", Argv: count("outside"), Inputs: &Inputs{Paths: []string{"src/"}}},
		{Name: "inside", Argv: count("inside"), Inputs: &Inputs{Paths: []string{"lib/"}}},
	}
	cache := t.TempDir()
	code, out := runGate(t, root, cache, steps)
	want := fmt.Sprintf("gate: outside: not cached: src/data.json reaches %s outside every keyed root\n", filepath.Join(outside, "data.json"))
	if code != 0 || !strings.Contains(out, want) {
		t.Fatalf("exit %d; want %q in:\n%s", code, want, out)
	}
	write(t, outside, "data.json", "not json at all\n")
	code, out = runGate(t, root, cache, steps)
	if code != 0 || runs(t, root, "outside") != 2 || strings.Contains(out, "== outside: ok, cached") {
		t.Fatalf("a step whose input leaves the checkout replayed (exit %d, %d runs):\n%s", code, runs(t, root, "outside"), out)
	}
	if runs(t, root, "inside") != 1 || !strings.Contains(out, "== inside: ok, cached") {
		t.Fatalf("a link inside the checkout did not replay:\n%s", out)
	}
	write(t, root, "docs/notes.md", "changed\n")
	if code, out = runGate(t, root, cache, steps); code != 0 || runs(t, root, "inside") != 2 {
		t.Fatalf("the file a link reaches changed, and the step replayed (exit %d):\n%s", code, out)
	}
}

// declaredSources are the program arguments of exec calls that are not
// literals, by "<function> <expression>", and what each one is.
var declaredSources = map[string]string{
	"toolVersion argv[0]":                                     "a tool the gate fingerprints: a declared host program, or a test's probe",
	"installation command":                                    "a tool the gate fingerprints",
	"resolveCommand command":                                  "a tool the gate fingerprints",
	"queryTool name":                                          "a tool the gate fingerprints, or the git that git --exec-path resolved",
	"scopeProbe command":                                      "systemd-run, as exec.LookPath resolved it",
	"enterScope command":                                      "systemd-run, as exec.LookPath resolved it",
	"execute argv[0]":                                         "a step's program, or the nsenter the sandbox wraps it in: TestHostProgramsDeclared checks every step",
	"startSandbox hold":                                       "cat, as exec.LookPath resolved it",
	"startSandbox bwrap":                                      "bwrap, as exec.LookPath resolved it",
	"TestSandboxPlanOfThisHost argv[0]":                       "a probe script wrapped by the sandbox: its nsenter",
	"TestMergeTierFailsClosed tool":                           "go, git, sh, cat or nsenter, as the test links them",
	"TestMergeTierSandboxRegression step.Argv[0]":             "a regression row's step launched outside the gate as a witness",
	"TestBudgetRecordDurationsScope binary":                   "the gate built by the test from this package",
	"testBinaryPackages goCommand":                            "the real go of TestMain (realTools)",
	"TestGroupProcessZombieLeaderOfRealPS python":             "python3, as exec.LookPath resolved it",
	"TestMain tool":                                           "go or git, to find the real tools",
	"probe realTools[\"go\"]":                                 "the real go of TestMain (realTools)",
	"TestRealGoEnvironmentReportsItsGoroot realTools[\"go\"]": "the real go of TestMain (realTools)",
	"sandboxPrograms name":                                    "a name from hostPrograms, looked up so that no mask hides it",
	"TestSandboxMasksContaminatedInputs argv[0]":              "a probe script wrapped by the sandbox: its nsenter",
	"TestSandboxAdmitsSymlinkRoots argv[0]":                   "a probe script wrapped by the sandbox: its nsenter",
	"TestSandboxArgvBindsAdmittedRoots argv[0]":               "a probe script wrapped by the sandbox: its nsenter",
	"TestSandboxModuleZipIsConstructed argv[0]":               "a probe script wrapped by the sandbox: its nsenter",
	"moduleHost goTool":                                       "the real go of TestMain (realTools)",
	"TestMergeTierInputContractRegression prime[0]":           "the regression step's own program, launched outside the gate to prime the host cache",
	"runOracleHelper os.Args[0]":                              "this test binary, re-run as a step of the output-oracle helper gate (TestBudgetScopeHelper)",
}

// scriptVariables are programs a step's script runs by a variable.
var scriptVariables = map[string]string{
	`"$bin"`: "the provenance analyzer the step builds from tools/provenance in the checkout",
}

// shellWords that start a command but run no program.
var shellWords = []string{"if", "then", "else", "elif", "fi", "do", "done", "while", "until", "for", "case", "esac", "{", "}", "!",
	"echo", "exit", "test", "[", "printf", "cd", "set", "export", "read", "local", "return", "shift", ":", "wait", "trap"}

// scriptPrograms are the programs a shell script names as commands: the
// first word of every command, past keywords, builtins and assignments,
// with quotes and command substitutions followed.
func scriptPrograms(script string) []string {
	type segment struct {
		text    string
		command bool
	}
	var segments []segment
	var current strings.Builder
	command := true
	cut := func(next bool) {
		segments = append(segments, segment{current.String(), command})
		current.Reset()
		command = next
	}
	// The context stack: 'n' normal (a command), 'd' double quotes, 's'
	// single quotes.
	stack := []byte{'n'}
	for i := 0; i < len(script); i++ {
		c := script[i]
		top := stack[len(stack)-1]
		switch {
		case top == 's':
			if c == '\'' {
				stack = stack[:len(stack)-1]
			}
		case c == '\\':
			current.WriteByte(c)
			i++
			if i < len(script) {
				current.WriteByte(script[i])
			}
			continue
		case c == '$' && i+1 < len(script) && script[i+1] == '(':
			i++
			stack = append(stack, 'n')
			cut(true)
			continue
		case top == 'd':
			if c == '"' {
				stack = stack[:len(stack)-1]
			}
		case c == '\'':
			stack = append(stack, 's')
		case c == '"':
			stack = append(stack, 'd')
		case c == '(':
			stack = append(stack, 'n')
			cut(true)
			continue
		case c == ')':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			// What follows continues the word the substitution was in.
			cut(false)
			continue
		case c == ';' || c == '&' || c == '|' || c == '\n':
			cut(true)
			continue
		}
		current.WriteByte(c)
	}
	cut(true)
	var programs []string
	for _, s := range segments {
		if !s.command {
			continue
		}
		for _, word := range strings.Fields(s.text) {
			if slices.Contains(shellWords, word) {
				if word == "{" || word == "!" || word == "if" || word == "then" || word == "else" || word == "elif" || word == "do" || word == "while" || word == "until" {
					continue
				}
				break
			}
			if eq := strings.IndexByte(word, '='); eq > 0 && !strings.ContainsAny(word[:eq], `"'$`) {
				continue
			}
			programs = append(programs, word)
			break
		}
	}
	return programs
}

// TestHostProgramsDeclared: every program this package's code and tests
// start is a declared host program (hostPrograms) or a declared non-literal
// source; every step's program, and every program its shell script runs, is
// declared; and the whole-gate key fingerprints every declared program.
func TestHostProgramsDeclared(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	calls := 0
	// used are the declared sources a call names; an unused one is stale.
	used := map[string]bool{}
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				argument := -1
				switch pkg.Name + "." + selector.Sel.Name {
				case "exec.Command", "exec.LookPath", "syscall.Exec":
					argument = 0
				case "exec.CommandContext":
					argument = 1
				}
				if argument < 0 || len(call.Args) <= argument {
					return true
				}
				calls++
				program := call.Args[argument]
				if literal, ok := program.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					value, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.Contains(hostPrograms, value) {
						t.Errorf("%s: %s starts %q, which hostPrograms does not declare", fset.Position(call.Pos()), function.Name.Name, value)
					}
					return true
				}
				var text bytes.Buffer
				if err := printer.Fprint(&text, fset, program); err != nil {
					t.Fatal(err)
				}
				source := function.Name.Name + " " + text.String()
				used[source] = true
				if _, ok := declaredSources[source]; !ok {
					t.Errorf("%s: %s starts the program %s, which is neither a literal in hostPrograms nor a declared source (declaredSources)", fset.Position(call.Pos()), function.Name.Name, text.String())
				}
				return true
			})
		}
	}
	if calls < 20 {
		t.Fatalf("found %d calls that start programs; the check is vacuous", calls)
	}
	for source := range declaredSources {
		if !used[source] {
			t.Errorf("declaredSources names %q, which no call starts; remove it", source)
		}
	}

	_, steps := repositorySteps(t)
	named := map[string]bool{}
	for _, step := range steps {
		argv := step.Argv
		for len(argv) > 1 && argv[0] == "env" {
			named["env"] = true
			argv = argv[1:]
			for len(argv) > 0 && strings.Contains(argv[0], "=") {
				argv = argv[1:]
			}
		}
		programs := []string{argv[0]}
		if len(argv) >= 3 && argv[0] == "sh" && argv[1] == "-c" {
			programs = append(programs, scriptPrograms(argv[2])...)
		}
		for _, program := range programs {
			switch {
			case strings.HasPrefix(program, "./"):
				// A program built into the checkout: a file the key holds.
			case scriptVariables[program] != "":
			case slices.Contains(hostPrograms, program):
				named[program] = true
			default:
				t.Errorf("step %q runs %q, which hostPrograms does not declare", step.Name, program)
			}
		}
	}
	for _, program := range []string{"go", "gofmt", "git", "sh", "env", "cmp", "mv", "mkdir", "dirname", "mktemp"} {
		if !named[program] {
			t.Errorf("no step was found to run %s; the script reading is vacuous", program)
		}
	}

	host := (&gate{root: t.TempDir()}).collectHost("rss: test")
	for _, program := range hostPrograms {
		if !slices.ContainsFunc(host.facts, func(f replay.Fact) bool { return f.Name == "program "+program }) {
			t.Errorf("the whole-gate key does not fingerprint %s", program)
		}
	}
}

// collectorModel is the reference of TestWholeGateKeyCollector: the key
// inputs as the spec defines them, kept apart from the collector.
type collectorModel struct {
	files    map[string]*collectorFile
	dirs     map[string]bool
	index    map[string]string
	excluded map[string]bool
	head     int
	host     map[string]string
}

type collectorFile struct {
	content string
	perm    os.FileMode
	stat    int64
}

func (m *collectorModel) class(name string) replay.Class {
	switch {
	case m.index[name] != "":
		return replay.Tracked
	case m.excluded[name]:
		return replay.Ignored
	}
	return replay.Untracked
}

func (m *collectorModel) projection() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("head %d", m.head))
	for name, entry := range m.index {
		lines = append(lines, "index "+name+" "+entry)
	}
	for name, value := range m.host {
		lines = append(lines, "host "+name+" "+value)
	}
	for name := range m.dirs {
		lines = append(lines, "dir "+name)
	}
	for name, f := range m.files {
		switch class := m.class(name); class {
		case replay.Ignored:
			lines = append(lines, fmt.Sprintf("file %s ignored %d", name, f.stat))
		default:
			lines = append(lines, fmt.Sprintf("file %s %s %o %q", name, class, f.perm&keyedPermissions, f.content))
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// TestWholeGateKeyCollector runs generated operations on a real repository
// and host settings, and after each one requires that the collected
// whole-gate key changes exactly when the reference says a keyed input
// changed: files in each class, moves between classes, commits, directories,
// no-op touches and index refreshes, gate settings, received and unreceived
// variables, a tool replaced by a copy one byte apart, a declared program
// removed from PATH, and TMPDIR on another file system when there is one.
func TestWholeGateKeyCollector(t *testing.T) {
	root := fixture(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"GOFLAGS", "ERGO_GATE_COLLECTOR_PROBE", "GATE_COLLECTOR_UNRECEIVED"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	tool := probe(t, filepath.Join(t.TempDir(), "probe-tool"))
	link(t, bin, "probe-tool", tool)
	gone := probe(t, filepath.Join(t.TempDir(), "probe-gone"))
	link(t, bin, "probe-gone", gone)
	// The declared programs are the probes and sh: git's fingerprint hashes
	// its exec path (about 0.25 s a key on this host, measured), which
	// TestExecutableFingerprintsKeyTheirInstallations covers.
	g := &gate{root: root, cacheDir: t.TempDir(), programs: []string{"sh", "probe-tool", "probe-gone"}}
	m := &collectorModel{files: map[string]*collectorFile{}, dirs: map[string]bool{"src": true, "docs": true}, index: map[string]string{}, excluded: map[string]bool{}, host: map[string]string{}}
	stamp := int64(1_000_000_000)
	touch := func(name string) {
		stamp++
		when := time.Unix(stamp, 0)
		if err := os.Chtimes(filepath.Join(root, name), when, when); err != nil {
			t.Fatal(err)
		}
		m.files[name].stat = stamp
	}
	blob := func(name string) string {
		f := m.files[name]
		return fmt.Sprintf("%x %t", sha256.Sum256([]byte(f.content)), f.perm&0o100 != 0)
	}
	for name, content := range map[string]string{".gitignore": "/runs/\n", "src/input.txt": "one\n", "docs/notes.md": "notes\n"} {
		m.files[name] = &collectorFile{content: content, perm: 0o644}
		touch(name)
		m.index[name] = blob(name)
	}
	writeExclude := func() {
		var lines []string
		for name := range m.excluded {
			lines = append(lines, "/"+name)
		}
		slices.Sort(lines)
		write(t, root, ".git/info/exclude", strings.Join(lines, "\n")+"\n")
	}
	pool := []string{"src/a.txt", "docs/b.md", "top.txt", "deep/x/y.txt", "src/input.txt", "docs/notes.md"}
	emptyDirs := []string{"empty", "nest/inner"}
	contents := []string{"", "one\n", "two\n", "three three\n"}
	shm := ""
	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		if dir, err := os.MkdirTemp("/dev/shm", "gate-collector-"); err == nil {
			shm = dir
			t.Cleanup(func() { os.RemoveAll(dir) })
		}
	}
	diskTemp := t.TempDir()
	t.Setenv("TMPDIR", diskTemp)
	key := func() (string, *replay.Facts) {
		facts, err := g.collectFacts("rss: test")
		if err != nil {
			t.Fatal(err)
		}
		return facts.Key(), facts
	}
	rng := rand.New(rand.NewPCG(20261104, 1))
	pickOf := func(items []string) string { return items[rng.IntN(len(items))] }
	existing := func() []string {
		var names []string
		for name := range m.files {
			if name != ".gitignore" {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		return names
	}
	ensureDirs := func(name string) {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			m.dirs[dir] = true
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stampTool := 0
	operations := []struct {
		weight int
		run    func() string
	}{
		{6, func() string { // add a file in a class
			name := pickOf(pool)
			if m.files[name] != nil {
				return "add: exists"
			}
			content := pickOf(contents)
			ensureDirs(name)
			write(t, root, name, content)
			m.files[name] = &collectorFile{content: content, perm: 0o644}
			touch(name)
			switch rng.IntN(3) {
			case 0:
				delete(m.excluded, name)
				writeExclude()
				gitIn(t, root, "add", "--", name)
				m.index[name] = blob(name)
			case 1:
				m.excluded[name] = true
				writeExclude()
			}
			return "add " + name + " " + string(m.class(name))
		}},
		{6, func() string { // edit
			names := existing()
			if len(names) == 0 {
				return "edit: none"
			}
			name := pickOf(names)
			content := pickOf(contents)
			write(t, root, name, content)
			if err := os.Chmod(filepath.Join(root, name), m.files[name].perm); err != nil {
				t.Fatal(err)
			}
			m.files[name].content = content
			touch(name)
			return "edit " + name
		}},
		{2, func() string { // delete
			names := existing()
			if len(names) == 0 {
				return "delete: none"
			}
			name := pickOf(names)
			if err := os.Remove(filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			delete(m.files, name)
			return "delete " + name
		}},
		{2, func() string { // rename
			names := existing()
			to := pickOf(pool)
			if len(names) == 0 || m.files[to] != nil {
				return "rename: none"
			}
			from := pickOf(names)
			ensureDirs(to)
			if err := os.Rename(filepath.Join(root, from), filepath.Join(root, to)); err != nil {
				t.Fatal(err)
			}
			m.files[to] = m.files[from]
			delete(m.files, from)
			touch(to)
			return "rename " + from + " " + to
		}},
		{3, func() string { // chmod a keyed or an unkeyed bit
			names := existing()
			if len(names) == 0 {
				return "chmod: none"
			}
			name := pickOf(names)
			bit := []os.FileMode{0o100, 0o010, 0o001, 0o040, 0o020, 0o004, 0o002}[rng.IntN(7)]
			m.files[name].perm ^= bit
			if err := os.Chmod(filepath.Join(root, name), m.files[name].perm); err != nil {
				t.Fatal(err)
			}
			touch(name)
			return fmt.Sprintf("chmod %s %o", name, m.files[name].perm)
		}},
		{3, func() string { // git add, git rm --cached, an ignore rule, its removal
			names := existing()
			if len(names) == 0 {
				return "class: none"
			}
			name := pickOf(names)
			switch rng.IntN(4) {
			case 0:
				gitIn(t, root, "add", "-f", "--", name)
				m.index[name] = blob(name)
			case 1:
				// -f: the index entry may differ from both the file and
				// HEAD; --cached keeps the file.
				if m.index[name] != "" {
					gitIn(t, root, "rm", "-q", "-f", "--cached", "--", name)
					delete(m.index, name)
				}
			case 2:
				if m.index[name] != "" {
					gitIn(t, root, "rm", "-q", "-f", "--cached", "--", name)
					delete(m.index, name)
				}
				m.excluded[name] = true
				writeExclude()
			case 3:
				delete(m.excluded, name)
				writeExclude()
			}
			return "class " + name + " " + string(m.class(name))
		}},
		{1, func() string { // a commit (the tree may be the same)
			gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "op")
			m.head++
			return "commit"
		}},
		{2, func() string { // an empty directory, made or removed
			name := pickOf(emptyDirs)
			full := filepath.Join(root, name)
			if m.dirs[name] {
				if err := os.Remove(full); err != nil {
					t.Fatal(err)
				}
				delete(m.dirs, name)
				return "rmdir " + name
			}
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			for dir := name; dir != "."; dir = path.Dir(dir) {
				m.dirs[dir] = true
			}
			return "mkdir " + name
		}},
		{3, func() string { // no-ops: a touch, a rewrite of the same bytes, an index refresh
			names := existing()
			switch rng.IntN(3) {
			case 0:
				if len(names) > 0 {
					name := pickOf(names)
					touch(name)
					return "touch " + name
				}
			case 1:
				if len(names) > 0 {
					name := pickOf(names)
					write(t, root, name, m.files[name].content)
					if err := os.Chmod(filepath.Join(root, name), m.files[name].perm); err != nil {
						t.Fatal(err)
					}
					touch(name)
					return "rewrite " + name
				}
			}
			gitIn(t, root, "update-index", "-q", "--refresh")
			gitIn(t, root, "status", "--porcelain")
			return "refresh the index"
		}},
		{3, func() string { // the gate's settings and the environment
			value := strconv.Itoa(rng.IntN(3))
			switch rng.IntN(3) {
			case 0:
				t.Setenv("ERGO_GATE_COLLECTOR_PROBE", value)
				m.host["setting"] = value
				return "setting " + value
			case 1:
				t.Setenv("GOFLAGS", "-tags=probe"+value)
				m.host["GOFLAGS"] = value
				return "GOFLAGS " + value
			}
			t.Setenv("GATE_COLLECTOR_UNRECEIVED", value)
			return "unreceived variable " + value
		}},
		{1, func() string { // the tool replaced by a copy one byte apart
			stampTool++
			copied := filepath.Join(t.TempDir(), "probe-tool")
			copyFile(t, tool, copied)
			rebuildProbe(t, copied, fmt.Sprintf("probe-build-marker-%c", 'a'+byte(stampTool%26)))
			link(t, bin, "probe-tool", copied)
			m.host["tool"] = strconv.Itoa(stampTool)
			return "tool copy " + strconv.Itoa(stampTool)
		}},
		{1, func() string { // a declared program removed from PATH, or back
			if m.host["gone"] == "" {
				if err := os.Remove(filepath.Join(bin, "probe-gone")); err != nil {
					t.Fatal(err)
				}
				m.host["gone"] = "removed"
				return "remove probe-gone"
			}
			link(t, bin, "probe-gone", gone)
			delete(m.host, "gone")
			return "restore probe-gone"
		}},
		{1, func() string { // TMPDIR on another file system
			if shm == "" {
				return "no second file system"
			}
			if m.host["tmp"] == "" {
				t.Setenv("TMPDIR", shm)
				m.host["tmp"] = "shm"
				return "TMPDIR on tmpfs"
			}
			t.Setenv("TMPDIR", diskTemp)
			delete(m.host, "tmp")
			return "TMPDIR on disk"
		}},
	}
	total := 0
	for _, op := range operations {
		total += op.weight
	}
	before, beforeFacts := key()
	reference := m.projection()
	var log []string
	for i := range 220 {
		roll := rng.IntN(total)
		var description string
		for _, op := range operations {
			if roll < op.weight {
				description = op.run()
				break
			}
			roll -= op.weight
		}
		log = append(log, description)
		after, afterFacts := key()
		next := m.projection()
		if (after != before) != (next != reference) {
			t.Fatalf("operation %d %q: the key changed %t, the reference says %t; inputs that differ: %q\noperations: %q", i, description, after != before, next != reference, replay.Diff(beforeFacts, afterFacts, 5), log)
		}
		before, beforeFacts, reference = after, afterFacts, next
	}
}
