package main

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The gate's cache policy (gate-testcache-gap spec Q-R4-1): uncached by
// default, cached by declared exception. Every step runs with -count=1 after
// the caller's GOFLAGS (stepEnvironment), so no go test it starts replays
// Go's test cache: one in its argv, one a script runs, one a program runs
// (go run -exec, -toolexec, -vettool), at any depth, as long as the process
// inherits its environment. The one exception is the hermetic step
// (Step.Hermetic): its packages are below hermeticTestRoot and
// TestCachedGatePackagesAreHermetic checks each of them.
//
// Rounds 1 to 3 of this unit read step argv for the shapes that start a go
// test, and each round found a shape the reader did not see: a process can
// start a process, and no static reader of argv sees every process a program
// starts. So the policy test reads the environment the gate builds for each
// step (commandEnvironment), not argv shapes.
//
// Threat model: the guard protects against an accidental gap, a new step or
// a new test whose go test could replay a stale pass. A step that sets
// GOFLAGS or -count on purpose could undo the default; that is out of scope,
// and the tripwire (cacheTripwire) makes such a change fail a test, so it is
// visible in review.
//
// Hygiene from round 3 stays: a step starts only a program on stepPrograms,
// sets only a variable on stepEnvironmentNames, and runs a shell script only
// when it is a reviewed script (reviewed.go).

// stepPrograms are the programs a step's argv may start, by the exact name
// it uses.
var stepPrograms = map[string]string{
	"go":         "the go command",
	"env":        "env NAME=VALUE... COMMAND, each NAME one of stepEnvironmentNames",
	"timeout":    "timeout DURATION COMMAND",
	"sh":         "sh -c SCRIPT [NAME [ARG...]], SCRIPT one of reviewedScripts",
	"./bin/ergo": "the compiler the gate builds",
}

// stepEnvironmentNames are the variables a step may set, in an env prefix or
// in Step.Env: the ones the gate sets. ERGO_REPO_ROOT gives the kernel tests
// the checkout; CGO_ENABLED=0 builds the hermetic gate packages;
// ERGO_TEST_CLI names to a cliTestPackages step the bin/ergo that the step's
// need (buildCLIStep) wrote in this run. Its value is a constant relative
// path, and Step.Env is a part of both tiers' keys (TestCLIPathIsInTheStepKey),
// so a changed value cannot replay a pass recorded with another CLI.
// ERGO_CLOSURE_SWEEP selects what a closure sweep step sweeps (sweepStep):
// all, or one package directory; it is a selection, not a cache or flag
// variable, and Step.Env keys it the same way.
var stepEnvironmentNames = []string{"CGO_ENABLED", "ERGO_REPO_ROOT", "ERGO_TEST_CLI", "ERGO_CLOSURE_SWEEP"}

// timeoutDuration is the DURATION operand of timeout, written literally.
var timeoutDuration = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)

// hygieneProblems returns where a step leaves the program allowlist, the
// variable allowlist or the reviewed scripts.
func hygieneProblems(step *Step, scripts []*reviewedScript) []string {
	var problems []string
	for _, entry := range step.Env {
		if name, _, _ := strings.Cut(entry, "="); !slices.Contains(stepEnvironmentNames, name) {
			problems = append(problems, fmt.Sprintf("step %q sets %q, which is not one of the variables a step may set", step.Name, name))
		}
	}
	argv := step.Argv
	for {
		refuse := func(problem string) []string {
			return append(problems, fmt.Sprintf("step %q: %s: %s", step.Name, problem, strings.Join(step.Argv, " ")))
		}
		if len(argv) == 0 {
			return refuse("no command")
		}
		if _, ok := stepPrograms[argv[0]]; !ok {
			return refuse("the program " + strconv.Quote(argv[0]) + " is not one a step may start by that name")
		}
		switch argv[0] {
		case "env":
			rest := argv[1:]
			for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && strings.Contains(rest[0], "=") {
				if name, _, _ := strings.Cut(rest[0], "="); !slices.Contains(stepEnvironmentNames, name) {
					return refuse("env sets " + strconv.Quote(name) + ", which is not one of the variables a step may set")
				}
				rest = rest[1:]
			}
			argv = rest
			continue
		case "timeout":
			if len(argv) < 3 || !timeoutDuration.MatchString(argv[1]) {
				return refuse("timeout is read only as timeout DURATION COMMAND")
			}
			argv = argv[2:]
			continue
		case "sh":
			if len(argv) < 3 || argv[1] != "-c" || !slices.ContainsFunc(scripts, func(s *reviewedScript) bool { return s.text == argv[2] }) {
				return refuse("a shell runs only sh -c SCRIPT with a reviewed script (reviewedScripts)")
			}
		}
		return problems
	}
}

// countFlag finds a test count flag in text: -count, --count, -test.count or
// --test.count, its separator and its value.
var countFlag = regexp.MustCompile(`(?:^|[^\w.-])(--?(?:test\.)?count)\b(=?)([^\s'"&;|)]*)`)

// cacheTripwire returns why text, a step's argument, a step's variable or a
// reviewed script, could undo the uncached default: it names GOFLAGS, or it
// gives a count flag anything but =1. The check is literal on purpose: it
// does not decide whether the text is a go command, so every such word is
// seen in review.
func cacheTripwire(text string) []string {
	var hits []string
	if strings.Contains(text, "GOFLAGS") {
		hits = append(hits, "names GOFLAGS")
	}
	for _, match := range countFlag.FindAllStringSubmatch(text, -1) {
		if match[2] != "=" || match[3] != "1" {
			hits = append(hits, "gives "+strings.TrimSpace(match[0])+": only -count=1")
		}
	}
	return hits
}

// cacheableTestFlags are the flags the hermetic step may pass, with whether
// each takes a value. Each is one of cmd/go's cacheable test flags.
var cacheableTestFlags = map[string]bool{"-run": true, "-skip": true, "-timeout": true, "-parallel": true, "-v": false, "-short": false, "-failfast": false}

// hermeticCommandProblems returns where a hermetic step is not a plain go
// test of gate packages below hermeticTestRoot, with cacheable flags and the
// hermetic environment. TestCachedGatePackagesAreHermetic checks each
// package.
func hermeticCommandProblems(module string, step *Step) []string {
	var problems []string
	refuse := func(problem string) {
		problems = append(problems, fmt.Sprintf("hermetic step %q: %s", step.Name, problem))
	}
	if !runsGoTest(step) {
		refuse("not a plain go test: " + strings.Join(step.Argv, " "))
		return problems
	}
	packages := 0
	for i := 2; i < len(step.Argv); i++ {
		arg := step.Argv[i]
		if !strings.HasPrefix(arg, "-") {
			relative, ok := strings.CutPrefix(arg, module+"/")
			if !ok || !strings.HasPrefix(relative, hermeticTestRoot) || slices.Contains(hostTestPackages, relative) || strings.Contains(relative, "...") {
				refuse(arg + " is not a gate package below " + hermeticTestRoot)
			}
			packages++
			continue
		}
		name, _, hasValue := strings.Cut(arg, "=")
		takesValue, ok := cacheableTestFlags[name]
		if !ok {
			refuse("passes " + arg + ", which is not a cacheable test flag")
			continue
		}
		if takesValue && !hasValue {
			i++
		}
	}
	if packages == 0 {
		refuse("names no package")
	}
	for _, entry := range step.Env {
		if !slices.Contains(hermeticEnvironment, entry) {
			refuse("sets " + entry + ", which is not the hermetic environment")
		}
	}
	return problems
}

// lastCount returns the last count flag in a GOFLAGS value, which is the one
// cmd/go applies.
func lastCount(flags string) string {
	last := ""
	for _, field := range strings.Fields(flags) {
		if countFlag.MatchString(field) {
			last = field
		}
	}
	return last
}

// cacheProblems returns why the gate may run step with a go test that
// replays Go's test cache: a tripwire word in its argv or variables, a
// hermetic step that is not a plain go test of hermetic gate packages, or a
// step whose environment, as the gate builds it, lacks -count=1 as the last
// count in GOFLAGS.
func cacheProblems(g *gate, module string, step *Step) []string {
	var problems []string
	for _, text := range append(slices.Clone(step.Argv), step.Env...) {
		for _, hit := range cacheTripwire(text) {
			problems = append(problems, fmt.Sprintf("step %q %s in %q", step.Name, hit, text))
		}
	}
	if step.Hermetic {
		return append(problems, hermeticCommandProblems(module, step)...)
	}
	flags, _ := environmentVariable(g.commandEnvironment(step), "GOFLAGS")
	if lastCount(flags) != uncachedTestFlag {
		problems = append(problems, fmt.Sprintf("step %q runs with GOFLAGS %q, which does not end its count with %s", step.Name, flags, uncachedTestFlag))
	}
	return problems
}

// declaredGateSteps returns the checkout root and every step the gate
// declares over the packages go list finds now, so a new package, shard or
// step is checked when it appears.
func declaredGateSteps(t *testing.T) (string, []*Step) {
	t.Helper()
	root := checkoutRoot(t)
	packages, err := goPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := declareSteps(root, packages)
	if err != nil {
		t.Fatal(err)
	}
	return root, steps
}

// TestTestCommandsHaveCacheCoverage proves the policy over every step the
// gate declares and over the caller's GOFLAGS: each step's command
// environment, as commandEnvironment builds it, ends its count with
// -count=1 after the caller's flags, or the step is the hermetic exception;
// no step argument, step variable or reviewed script trips the tripwire; and
// every step passes the hygiene allowlists. Then it adds steps of each shape
// rounds 1 to 3 found (wrappers, scripts, programs that start a go test) and
// checks the policy's verdict against a table that does not read argv.
func TestTestCommandsHaveCacheCoverage(t *testing.T) {
	root, steps := declaredGateSteps(t)
	module := modulePath(root)

	hermeticSteps := 0
	for _, step := range steps {
		if step.Hermetic {
			hermeticSteps++
		}
		if problems := hygieneProblems(step, reviewedScripts); len(problems) > 0 {
			t.Errorf("step %q leaves the hygiene allowlists:\n%s", step.Name, strings.Join(problems, "\n"))
		}
	}
	if hermeticSteps != 1 || len(steps)-hermeticSteps < 10 {
		t.Fatalf("the gate declares %d hermetic and %d other steps; want one hermetic exception and the rest", hermeticSteps, len(steps)-hermeticSteps)
	}
	for _, script := range reviewedScripts {
		if hits := cacheTripwire(script.text); len(hits) > 0 {
			t.Errorf("reviewed script %q: %q", script.name, hits)
		}
	}

	// The property, under each caller GOFLAGS: unset, empty, a flag of its
	// own, a count the default must win over, several.
	type caller struct {
		name  string
		set   bool
		flags string
	}
	callers := []caller{{"unset", false, ""}, {"empty", true, ""}, {"-mod=mod", true, "-mod=mod"}, {"-count=0", true, "-count=0"}, {"several", true, "-mod=mod -count=2 -v"}, {"-p=64", true, "-p=64"}}
	for _, c := range callers {
		t.Run("caller GOFLAGS "+c.name, func(t *testing.T) {
			t.Setenv("GOFLAGS", c.flags)
			if !c.set {
				os.Unsetenv("GOFLAGS")
			}
			g := &gate{root: root}
			for _, step := range steps {
				if problems := cacheProblems(g, module, step); len(problems) > 0 {
					t.Errorf("%s", strings.Join(problems, "\n"))
				}
				flags, set := environmentVariable(g.commandEnvironment(step), "GOFLAGS")
				// The gate's -p bound follows the caller's flags in every
				// step (stepGOFLAGS), and the uncached default follows it.
				bounded := stepGOFLAGS(c.flags, stepCPUs())
				want, wantSet := bounded+" "+uncachedTestFlag, true
				if step.Hermetic {
					want = bounded
				}
				if flags != want || set != wantSet {
					t.Errorf("step %q (hermetic %t) runs with GOFLAGS %q (set %t), want the caller's flags %q with the default: %q (set %t)", step.Name, step.Hermetic, flags, set, c.flags, want, wantSet)
				}
			}
		})
	}

	// Added steps. Each form starts a go test with no count of its own, in
	// a way rounds 1 to 3 found a reader of argv could miss. The policy
	// admits every one as an ordinary step, because the environment carries
	// -count=1 whatever the shape, and refuses every one as a hermetic step,
	// but the plain go test of a hermetic gate package. The hygiene
	// allowlists, which do not decide the policy, refuse the shapes they do
	// not know.
	g := &gate{root: root}
	hermeticPackage := module + "/scripts/gate/replay"
	inner := `go test -C tools/provenance ./... && exec "$0" "$@"`
	execShell := "sh -c '" + inner + "'"
	b1 := "go test -C tools/provenance ./..."
	forms := []struct {
		name     string
		argv     []string
		env      []string
		hermetic bool // the plain hermetic form: the one admitted as hermetic
		hygienic bool // admitted by the hygiene allowlists
	}{
		{"plain hermetic gate package", []string{"go", "test", testTimeout, hermeticPackage}, hermeticEnvironment, true, true},
		{"plain main package", []string{"go", "test", testTimeout, module + "/internal/testenv"}, nil, false, true},
		{"go -C DIR test", []string{"go", "-C", "tools/provenance", "test", "./..."}, nil, false, true},
		{"go test -C DIR", []string{"go", "test", "-C", "tools/provenance", "./..."}, nil, false, true},
		{"env prefix", []string{"env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x", "go", "test", "./..."}, nil, false, true},
		{"timeout", []string{"timeout", "900", "go", "test", "-C", "tools/provenance", "./..."}, nil, false, true},
		{"reviewed script with a path argument", provenanceScript.command("provenance", "/usr/local/go/bin/go", "/nonexistent/loop-probe-x"), nil, false, true},
		// Round 1 (M1): shells and scripts nobody reviewed.
		{"M1 timeout sh -c", []string{"timeout", "900", "sh", "-c", b1}, nil, false, false},
		{"M1 dash -c", []string{"dash", "-c", b1}, nil, false, false},
		{"M1 sh -c \"$@\"", []string{"sh", "-c", `"$@"`, "sh", "go", "test", "-C", "tools/provenance", "./..."}, nil, false, false},
		{"M1 sh -c exec \"$@\"", []string{"sh", "-c", `exec "$@"`, "sh", "go", "test", "-C", "tools/provenance", "./..."}, nil, false, false},
		// Round 2 (B1): a go the shell expands.
		{"r2 B1 ${GO:-go}", []string{"sh", "-c", "${GO:-go} -C tools/provenance test ./..."}, nil, false, false},
		{"r2 B1 a command in an env variable, eval", []string{"env", "X=" + b1, "sh", "-c", `eval "$X"`}, nil, false, false},
		{"r2 B1 a glob that names go", []string{"sh", "-c", "/usr/local/bin/g[o] -C tools/provenance test ./..."}, nil, false, false},
		// Round 3 (B1): a program that starts a go test.
		{"r3 B1 go run -exec", []string{"go", "run", "-exec=" + execShell, "./cmd/wayfinder", "check", "--snapshot"}, nil, false, true},
		{"r3 B1 go run -exec, separate", []string{"go", "run", "-exec", execShell, "./cmd/wayfinder", "check", "--snapshot"}, nil, false, true},
		{"r3 B1 go test -count=1 -exec", []string{"go", "test", "-count=1", "-exec=" + execShell, "./scripts/gate/replay"}, nil, false, true},
		{"r3 B1 go build -toolexec", []string{"go", "build", "-toolexec=" + execShell, "-o", "/nonexistent/loop-probe-x/w", "./cmd/wayfinder"}, nil, false, true},
		{"r3 B1 go vet -toolexec", []string{"go", "vet", "-toolexec=" + execShell, "./scripts/gate/replay"}, nil, false, true},
		{"r3 B1 go vet -vettool", []string{"go", "vet", "-vettool=/nonexistent/loop-probe-x", "./..."}, nil, false, true},
		{"r3 B1 staged build with a -toolexec argument", stagedBuildScript.command("stagedbuild", "/nonexistent/loop-probe-x/a", "/nonexistent/loop-probe-x/b", "-toolexec="+execShell), nil, false, true},
		{"r3 argv path to go", []string{"/usr/local/go/bin/go", "test", "-C", "tools/provenance", "./..."}, nil, false, false},
		{"r3 env -- go", []string{"env", "--", "go", "test", "-C", "tools/provenance", "./..."}, nil, false, false},
		{"r3 env GO=go", []string{"env", "GO=go", "go", "test", "./..."}, nil, false, false},
	}
	for _, f := range forms {
		for _, hermetic := range []bool{false, true} {
			step := &Step{Name: "policy probe", Argv: f.argv, Env: f.env, Hermetic: hermetic}
			admitted := len(cacheProblems(g, module, step)) == 0
			if want := !hermetic || f.hermetic; admitted != want {
				t.Errorf("%s (hermetic %t): the policy admits=%t, want %t: %q", f.name, hermetic, admitted, want, cacheProblems(g, module, step))
			}
		}
		step := &Step{Name: "policy probe", Argv: f.argv, Env: f.env}
		if hygienic := len(hygieneProblems(step, reviewedScripts)) == 0; hygienic != f.hygienic {
			t.Errorf("%s: the hygiene allowlists admit=%t, want %t: %q", f.name, hygienic, f.hygienic, hygieneProblems(step, reviewedScripts))
		}
	}

	// The tripwire: an override in an argument, a variable or a script fails
	// the policy, as an ordinary step and as a hermetic one.
	tripped := []struct {
		name string
		argv []string
		env  []string
	}{
		{"env GOFLAGS", []string{"env", "GOFLAGS=", "go", "test", "./..."}, nil},
		{"Step.Env GOFLAGS", []string{"go", "test", "./..."}, []string{"GOFLAGS=-mod=mod"}},
		{"GOFLAGS in a script", []string{"sh", "-c", "GOFLAGS= go test ./..."}, nil},
		{"unset GOFLAGS in a script", []string{"sh", "-c", "unset GOFLAGS; go test ./..."}, nil},
		{"go test -count=0", []string{"go", "test", "-count=0", "./..."}, nil},
		{"go test -count 1, separate", []string{"go", "test", "-count", "1", "./..."}, nil},
		{"go test -count=10", []string{"go", "test", "-count=10", "./..."}, nil},
		{"go test --count=2", []string{"go", "test", "--count=2", "./..."}, nil},
		{"go test -test.count=0", []string{"go", "test", "-test.count=0", "./..."}, nil},
		{"-count=0 in a script", []string{"sh", "-c", "go test -C tools/provenance -count=0 ./..."}, nil},
		{"-count=0 in an -exec value", []string{"go", "run", "-exec=sh -c 'go test -count=0 ./... && exec \"$0\"'", "./cmd/wayfinder"}, nil},
		{"-count=2 in Step.Env", []string{"go", "test", "./..."}, []string{"ERGO_REPO_ROOT=-count=2"}},
	}
	for _, c := range tripped {
		for _, hermetic := range []bool{false, true} {
			if problems := cacheProblems(g, module, &Step{Name: "tripwire probe", Argv: c.argv, Env: c.env, Hermetic: hermetic}); !slices.ContainsFunc(problems, func(p string) bool {
				return strings.Contains(p, "GOFLAGS in") || strings.Contains(p, "only -count=1")
			}) {
				t.Errorf("%s (hermetic %t): the tripwire does not fire: %q", c.name, hermetic, problems)
			}
		}
	}
	for _, text := range []string{"GOFLAGS= go test ./...", "go test -count=0 ./..."} {
		if len(cacheTripwire(text)) == 0 {
			t.Errorf("the tripwire passes a reviewed script %q", text)
		}
	}
	for _, text := range []string{"go test -count=1 ./...", `"-count=1"`, "x-count=0", "go test -run Count ./..."} {
		if hits := cacheTripwire(text); len(hits) > 0 {
			t.Errorf("the tripwire fires on %q: %q", text, hits)
		}
	}

	// Hygiene: the gate's own sh -c steps run reviewed scripts, and a
	// script with its first, middle or last byte changed is a script nobody
	// reviewed.
	scriptSteps := 0
	for _, step := range steps {
		if len(step.Argv) < 3 || step.Argv[0] != "sh" || step.Argv[1] != "-c" {
			continue
		}
		scriptSteps++
		text := step.Argv[2]
		for _, at := range []int{0, len(text) / 2, len(text) - 1} {
			changed := *step
			changed.Argv = slices.Clone(step.Argv)
			changed.Argv[2] = text[:at] + string(text[at]^1) + text[at+1:]
			if len(hygieneProblems(&changed, reviewedScripts)) == 0 {
				t.Errorf("the hygiene check admits step %q with byte %d of its script changed", step.Name, at)
			}
		}
	}
	if scriptSteps != len(reviewedScripts) {
		t.Errorf("the gate declares %d sh -c steps and %d reviewed scripts", scriptSteps, len(reviewedScripts))
	}
	for _, step := range []*Step{
		{Argv: []string{"make", "test"}},
		{Argv: []string{"bash", "-c", "true"}},
		{Argv: []string{"sh", "/nonexistent/loop-probe-x.sh"}},
		{Argv: []string{"sh", "-c", "true"}},
		{Argv: []string{"timeout", "$T", "go", "vet", "./..."}},
		{Argv: []string{"go", "vet", "./..."}, Env: []string{"GO=go"}},
		{Argv: []string{path.Join("/usr/local/go/bin", "go"), "vet", "./..."}},
	} {
		step.Name = "hygiene probe"
		if len(hygieneProblems(step, reviewedScripts)) == 0 {
			t.Errorf("the hygiene check admits %q env %q", step.Argv, step.Env)
		}
	}
}
