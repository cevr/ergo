package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A go test that Go's test cache may replay is sound only when something
// keys every input its tests read. The gate has three ways to give that:
//
//   - main-module observation: a package of the checkout's main module runs
//     under testinputs.Main, so cmd/go keys what the test process and its
//     children read (internal/testinputs/guard checks every such package);
//   - the hermetic gate packages: a package below hermeticTestRoot runs no
//     program and loads no library (TestCachedGatePackagesAreHermetic checks
//     each one a plain go test step lets Go cache);
//   - -count=1: the test never replays.
//
// cmd/go rechecks a cached result only against the files inside the
// package's own module. A test of another module (tools/provenance,
// prototypes/sketch9) that reads main-module sources therefore replays a
// stale pass unless it runs with -count=1 (gate-testcache-gap
// accept-base.log). The two guards above check only the go test forms they
// can see: a plain step argv in the checkout root. So this guard classifies
// every go test the gate declares, in each form a step can write it, and
// refuses any form it cannot classify.
//
// The guard reads no shell. A step's argv is read through an allowlist of
// programs (stepPrograms); a sh -c script is admitted only when it is a
// reviewed script (reviewedScripts, reviewed.go), and then the guard reads
// the runs the script declares, not its text.

// invocation is one go test command a step starts.
type invocation struct {
	step string
	// wrapped says how the command is written: "" for a plain step argv,
	// "env" behind an env prefix, "timeout" behind timeout, "shell" as a run
	// a reviewed script declares.
	wrapped string
	// directory is the -C value, before or after "test"; hasDirectory says
	// whether there was one.
	directory    string
	hasDirectory bool
	// count is the -count value; hasCount says whether there was one.
	count    string
	hasCount bool
	// flags are the flags other than -C and -count, each with its value.
	flags    [][]string
	packages []string
	// problem names a form the guard cannot classify.
	problem string
}

func (c invocation) String() string {
	return fmt.Sprintf("step %q (%s): go test dir=%q count=%q flags=%q packages=%q", c.step, orDefault(c.wrapped, "argv"), c.directory, c.count, c.flags, c.packages)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// cacheableTestFlags are the flags a cached go test step may pass, with
// whether each takes a value. Each is one of cmd/go's cacheable test flags,
// so none of them changes which module or files the result covers.
var cacheableTestFlags = map[string]bool{"-run": true, "-skip": true, "-timeout": true, "-parallel": true, "-v": false, "-short": false, "-failfast": false}

// stepPrograms are the programs a step's argv, or a run a reviewed script
// declares, may start, by the exact name it uses. A name that the shell would
// expand ($GO, ${GO:-go}, a glob) or another path to a program is not on the
// list, so the guard refuses it.
var stepPrograms = map[string]string{
	"go":              "go test is read; go build, go run and go vet run no go test of their own",
	"env":             "env NAME=VALUE... COMMAND, each NAME one of stepEnvironmentNames",
	"timeout":         "timeout DURATION COMMAND",
	"sh":              "sh -c SCRIPT [NAME [ARG...]], SCRIPT one of reviewedScripts",
	"./bin/ergo":      "the compiler the gate builds; ergo fmt runs no go test",
	provenanceProgram: "the analyzer provenanceScript builds and runs; it runs no go test",
}

// goCommands are the go commands other than go test that a step may run.
// None runs a go test of its own. go run starts a program the guard does not
// read; a go test that program starts is outside the guard (docs/tooling.md).
var goCommands = []string{"build", "run", "vet"}

// stepEnvironmentNames are the variables a step may set, in an env prefix or
// in Step.Env: the ones the gate sets. ERGO_REPO_ROOT gives the kernel tests
// the checkout; CGO_ENABLED=0 builds the hermetic gate packages. A variable
// that changes what go runs (GOFLAGS) or that a script could run as a command
// is not on the list.
var stepEnvironmentNames = []string{"CGO_ENABLED", "ERGO_REPO_ROOT"}

// shells are programs that run a script or a command they are given. An
// argument that names one, or go, is refused (namesCommand).
var shells = []string{"sh", "bash", "dash", "zsh", "ksh", "mksh", "ash", "yash", "posh", "fish", "csh", "tcsh", "rc", "busybox"}

// timeoutDuration is the DURATION operand of timeout, written literally.
var timeoutDuration = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)

// stepInvocations returns every go test command step starts, with a problem
// for each part of the step the guard cannot read.
func (k census) stepInvocations(step *Step) []invocation {
	found := k.commandInvocations(step.Name, step.Argv, "")
	for _, entry := range step.Env {
		if name, _, _ := strings.Cut(entry, "="); !slices.Contains(stepEnvironmentNames, name) {
			found = append(found, invocation{step: step.Name, problem: "the step sets " + strconv.Quote(name) + ", which is not one of the variables a step may set: " + entry})
		}
	}
	return found
}

// commandInvocations reads one command line, a step's argv or a run that a
// reviewed script declares:
//
//   - go test ...: the invocation itself (parseGoCommand);
//   - go build, go run, go vet: no go test;
//   - env NAME=VALUE... COMMAND: COMMAND, behind env, when each NAME is one
//     of stepEnvironmentNames;
//   - timeout DURATION COMMAND: COMMAND, behind timeout;
//   - sh -c SCRIPT [NAME [ARG...]]: the runs SCRIPT declares, behind shell,
//     when SCRIPT is byte for byte one of the census's reviewed scripts;
//   - the gate's own tools (stepPrograms): no go test.
//
// Any other program is refused, and so is any argument a program is given
// that names go or a shell: the guard does not read what the program does
// with it. Fail closed until classified.
func (k census) commandInvocations(step string, argv []string, wrapped string) []invocation {
	refuse := func(problem string) []invocation {
		return []invocation{{step: step, wrapped: wrapped, problem: problem + ": " + strings.Join(argv, " ")}}
	}
	if len(argv) == 0 {
		return refuse("no command")
	}
	program := argv[0]
	if _, ok := stepPrograms[program]; !ok {
		return refuse("the program " + strconv.Quote(program) + " is not one a step may start by that name")
	}
	switch program {
	case "go":
		if c, ok := parseGoCommand(step, argv, wrapped); ok {
			return []invocation{c}
		}
		if !slices.Contains(goCommands, goCommand(argv)) {
			return refuse("a go command the guard does not read")
		}
	case "env":
		rest := argv[1:]
		for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && strings.Contains(rest[0], "=") {
			if name, _, _ := strings.Cut(rest[0], "="); !slices.Contains(stepEnvironmentNames, name) {
				return refuse("env sets " + strconv.Quote(name) + ", which is not one of the variables a step may set")
			}
			rest = rest[1:]
		}
		return k.commandInvocations(step, rest, orDefault(wrapped, "env"))
	case "timeout":
		if len(argv) < 3 || !timeoutDuration.MatchString(argv[1]) {
			return refuse("timeout is read only as timeout DURATION COMMAND")
		}
		return k.commandInvocations(step, argv[2:], orDefault(wrapped, "timeout"))
	case "sh":
		if len(argv) < 3 || argv[1] != "-c" {
			return refuse("sh is read only as sh -c SCRIPT")
		}
		script := k.reviewed(argv[2])
		if script == nil {
			return refuse("a sh -c script that is not a reviewed script (reviewedScripts); the guard reads no shell")
		}
		var found []invocation
		for _, run := range script.runs {
			command := slices.Clone(run.argv)
			if run.program != "" && len(command) > 0 {
				command[0] = run.program
			}
			found = append(found, k.commandInvocations(step, command, "shell")...)
		}
		if slices.ContainsFunc(argv[3:], namesCommand) {
			found = append(found, refuse("the arguments of a sh -c script name go or a shell")...)
		}
		return found
	}
	if slices.ContainsFunc(argv[1:], namesCommand) {
		return refuse("a program is given go or a shell as an argument")
	}
	return nil
}

// reviewed returns the census's reviewed script whose text is script.
func (k census) reviewed(script string) *reviewedScript {
	for _, s := range k.scripts {
		if s.text == script {
			return s
		}
	}
	return nil
}

// namesCommand reports whether an argument, or a field of it split at white
// space, is go or a shell by its base name.
func namesCommand(argument string) bool {
	return slices.ContainsFunc(strings.Fields(argument), func(field string) bool {
		name := path.Base(field)
		return name == "go" || slices.Contains(shells, name)
	})
}

// goCommand returns the go command's name: its first argument after a leading
// -C DIR.
func goCommand(argv []string) string {
	i := 1
	switch {
	case i+1 < len(argv) && argv[i] == "-C":
		i += 2
	case i < len(argv) && strings.HasPrefix(argv[i], "-C="):
		i++
	}
	if i < len(argv) {
		return argv[i]
	}
	return ""
}

// parseGoCommand reads a go command line. It reports false for a go command
// that is not go test.
func parseGoCommand(step string, argv []string, wrapped string) (invocation, bool) {
	c := invocation{step: step, wrapped: wrapped}
	i := 1
	readDirectory := func() bool {
		switch {
		case i < len(argv) && argv[i] == "-C" && i+1 < len(argv):
			c.directory, c.hasDirectory = argv[i+1], true
			i += 2
			return true
		case i < len(argv) && strings.HasPrefix(argv[i], "-C="):
			c.directory, c.hasDirectory = strings.TrimPrefix(argv[i], "-C="), true
			i++
			return true
		}
		return false
	}
	readDirectory()
	if i >= len(argv) || argv[i] != "test" {
		return c, false
	}
	i++
	if !c.hasDirectory {
		readDirectory()
	}
	for ; i < len(argv); i++ {
		arg := argv[i]
		if arg == "-args" || arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			c.packages = append(c.packages, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		name = "-" + strings.TrimLeft(name, "-")
		switch name {
		case "-C":
			c.problem = "-C is not go test's first flag: " + strings.Join(argv, " ")
			return c, true
		case "-count", "-test.count":
			if !hasValue && i+1 < len(argv) {
				i++
				value = argv[i]
			}
			if c.hasCount {
				c.problem = "two -count flags: " + strings.Join(argv, " ")
			}
			c.count, c.hasCount = value, true
			continue
		}
		takesValue, known := cacheableTestFlags[name]
		flag := []string{name}
		if hasValue {
			flag = append(flag, value)
		} else if known && takesValue && i+1 < len(argv) {
			i++
			flag = append(flag, argv[i])
		}
		c.flags = append(c.flags, flag)
	}
	return c, true
}

// census is what the guard resolves a cached go test's packages against: the
// main module's path and the packages `go list ./...` finds in the checkout
// root, the domain internal/testinputs/guard walks. scripts are the reviewed
// scripts a sh -c step may run.
type census struct {
	root, module string
	packages     []string
	scripts      []*reviewedScript
}

// refusal returns why the gate may not run c as declared, or "".
func (k census) refusal(c invocation, step *Step) string {
	if c.problem != "" {
		return c.problem
	}
	if c.hasCount {
		if c.count != "1" {
			return fmt.Sprintf("-count=%s: only -count=1 runs the tests uncached; -count=0 runs none", c.count)
		}
		if c.hasDirectory {
			return k.moduleRefusal(c.directory)
		}
		return ""
	}
	// Go's test cache may replay it: only a plain step argv in the checkout
	// root is a form the observation and hermetic guards check.
	switch {
	case c.wrapped != "":
		return "a go test that Go may cache is behind " + c.wrapped + "; the observation and hermetic guards read only a plain step argv"
	case c.hasDirectory:
		return "a go test that Go may cache runs in " + c.directory + ", outside the checked main module; give it -count=1"
	}
	for _, flag := range c.flags {
		if _, ok := cacheableTestFlags[flag[0]]; !ok {
			return "a go test that Go may cache passes " + flag[0] + ", which no guard classifies"
		}
	}
	for _, entry := range step.Env {
		if !slices.Contains(hermeticEnvironment, entry) {
			return "a go test that Go may cache runs with " + entry + ", which no guard classifies"
		}
	}
	if len(c.packages) == 0 {
		return "a go test that Go may cache names no package"
	}
	for _, pkg := range c.packages {
		if _, ok := k.resolve(pkg); !ok {
			return "a go test that Go may cache names " + pkg + ", which is not a main-module package of the checkout root"
		}
	}
	return ""
}

// moduleRefusal checks that an uncached go test's -C names a module of the
// checkout.
func (k census) moduleRefusal(directory string) string {
	if !filepath.IsLocal(directory) {
		return "-C " + directory + " is not inside the checkout"
	}
	info, err := os.Stat(filepath.Join(k.root, directory, "go.mod"))
	if err != nil || !info.Mode().IsRegular() {
		return "-C " + directory + " names no module of the checkout"
	}
	return ""
}

// resolve returns the main-module packages a package argument names.
func (k census) resolve(pkg string) ([]string, bool) {
	if pkg == "./..." {
		return k.packages, len(k.packages) > 0
	}
	if relative, ok := strings.CutPrefix(pkg, "./"); ok {
		pkg = k.module + "/" + relative
	}
	if slices.Contains(k.packages, pkg) {
		return []string{pkg}, true
	}
	return nil, false
}

// class names the guard that answers for a cached package.
func (k census) class(pkg string) string {
	relative := strings.TrimPrefix(pkg, k.module+"/")
	if relative+"/" == hermeticTestRoot || strings.HasPrefix(relative, hermeticTestRoot) {
		return "hermetic"
	}
	return "observed"
}

// refusals runs the guard over steps.
func (k census) refusals(steps []*Step) []string {
	var found []string
	for _, step := range steps {
		for _, c := range k.stepInvocations(step) {
			if reason := k.refusal(c, step); reason != "" {
				found = append(found, c.String()+": "+reason)
			}
		}
	}
	return found
}

// withScripts returns the census with a different registry of reviewed
// scripts.
func (k census) withScripts(scripts []*reviewedScript) census {
	k.scripts = scripts
	return k
}

func repositoryCensus(t *testing.T) (census, []*Step) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	packages, err := goPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := declareSteps(root, packages)
	if err != nil {
		t.Fatal(err)
	}
	return census{root: root, module: modulePath(root), packages: packages, scripts: reviewedScripts}, steps
}

// TestTestCommandsHaveCacheCoverage classifies every go test the gate
// declares, over the packages go list finds now, so a new package, shard or
// step is classified when it appears. Then it generates additions and
// mutations in each form a step can write a go test (a plain argv, go -C DIR
// test, go test -C DIR, an env prefix, a reviewed script's declared run, a
// second command in the provenance script, and each wrapper the guard reads
// or must refuse), over each kind of target (an observed main-module package,
// a hermetic gate package, a package of another module, a missing module) and
// each count (none, 0, 1), and checks the guard's verdict against a table
// that does not parse commands.
func TestTestCommandsHaveCacheCoverage(t *testing.T) {
	t.Parallel()
	k, steps := repositoryCensus(t)
	if refusals := k.refusals(steps); len(refusals) > 0 {
		t.Fatalf("the gate declares go test commands without cache coverage:\n%s", strings.Join(refusals, "\n"))
	}

	// Every declared go test is seen, every package of every shard is
	// resolved and classified, and the census holds both classes.
	classes := map[string]int{}
	covered := map[string]bool{}
	for _, step := range steps {
		for _, c := range k.stepInvocations(step) {
			if c.hasDirectory {
				continue
			}
			for _, pkg := range c.packages {
				resolved, _ := k.resolve(pkg)
				for _, p := range resolved {
					covered[p] = true
					if !c.hasCount {
						classes[k.class(p)]++
					}
				}
			}
		}
	}
	for _, pkg := range k.packages {
		if !covered[pkg] {
			t.Errorf("no go test the gate declares resolves %s", pkg)
		}
	}
	if classes["observed"] == 0 || classes["hermetic"] == 0 {
		t.Fatalf("the census has %d observed and %d hermetic cached packages; the check is vacuous", classes["observed"], classes["hermetic"])
	}
	provenance := stepNamed(t, steps, "provenance analyzer")
	kernel := stepNamed(t, steps, "go test ./prototypes/sketch9/sketch (kernel)")
	controls := k.stepInvocations(provenance)
	if len(controls) != 1 || controls[0].directory != "tools/provenance" || controls[0].count != "1" || controls[0].wrapped != "shell" {
		t.Fatalf("the provenance script's go test is not read as one uncached test of tools/provenance: %v", controls)
	}

	// Generated additions and mutations.
	observed, hermeticPackage := "", ""
	for _, pkg := range k.packages {
		switch {
		case k.class(pkg) == "hermetic" && pkg != k.module+"/scripts/gate" && hermeticPackage == "":
			hermeticPackage = pkg
		case k.class(pkg) == "observed" && observed == "":
			observed = pkg
		}
	}
	type target struct {
		name, directory, pkg string
		env                  []string
		// module says whether the directory holds a module; main whether it
		// is the checkout root.
		module, main bool
	}
	targets := []target{
		{name: "observed main package", directory: ".", pkg: observed, module: true, main: true},
		{name: "hermetic gate package", directory: ".", pkg: hermeticPackage, env: hermeticEnvironment, module: true, main: true},
		{name: "added main package", directory: ".", pkg: k.module + "/internal/coverageprobe", module: true, main: true},
		{name: "unobserved nested module", directory: "tools/provenance", pkg: "./...", module: true},
		{name: "nested module by path", directory: ".", pkg: "ergo.local/prototype/tools/provenance", module: true, main: true},
		{name: "missing module", directory: "nonexistent/loop-probe-x", pkg: "./...", module: false},
	}
	line := func(command []string) string { return strings.Join(command, " ") }
	rest := func(command []string) string { return strings.Join(command[1:], " ") }
	// probeScript is a reviewed script that declares command as its run.
	probeScript := func(text string, command []string) *reviewedScript {
		return &reviewedScript{name: "coverage probe", text: text, runs: []scriptRun{{argv: command}}}
	}
	// The provenance script with command as a second go test, after its first
	// line.
	provenanceWith := func(command []string) string {
		return strings.Replace(provenanceScript.text, "&&\n", "&&\n"+line(command)+" &&\n", 1)
	}
	readable := map[string]bool{"argv": true, "go -C DIR test": true, "go test -C DIR": true, "env": true, "reviewed script": true, "second go test in the provenance script, reviewed": true}
	forms := []string{"argv", "go -C DIR test", "go test -C DIR", "env", "reviewed script", "second go test in the provenance script, reviewed", "second go test in the provenance script, not reviewed", "new step in shell, not reviewed"}
	// Wrappers run the go test -C DIR form. The guard can unwrap a readable
	// one, so it admits the command exactly where it admits the bare -C form
	// with -count=1. Every other wrapper hides the command, so the guard
	// refuses it at every count: the forms gate-testcache-gap rounds 1 (M1)
	// and 2 (B1) found admitted are among them. A wrapper with a script
	// registers script(command) as a reviewed script that declares command.
	type wrapper struct {
		name     string
		readable bool
		wrap     func(command []string) []string
		script   func(command []string) string
	}
	wrappers := []wrapper{
		{"timeout", true, func(c []string) []string { return append([]string{"timeout", "900"}, c...) }, nil},
		{"env ERGO_REPO_ROOT", true, func(c []string) []string {
			return append([]string{"env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x"}, c...)
		}, nil},
		{"nested wrappers", true, func(c []string) []string {
			return append([]string{"timeout", "900", "env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x", "timeout", "60"}, c...)
		}, nil},
		{"timeout sh -c, reviewed", true, func(c []string) []string { return []string{"timeout", "900", "sh", "-c", line(c)} }, line},
		{"env sh -c, reviewed", true, func(c []string) []string {
			return []string{"env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x", "sh", "-c", "cd . && " + line(c)}
		}, func(c []string) string { return "cd . && " + line(c) }},
		{"sh -c with a name and a path, reviewed", true, func(c []string) []string { return []string{"sh", "-c", line(c), "probe", "/nonexistent/loop-probe-x"} }, line},

		// Round 1 and round 2 forms: a script nobody reviewed, another shell,
		// positional arguments, a program the guard does not read.
		{"timeout sh -c, not reviewed", false, func(c []string) []string { return []string{"timeout", "900", "sh", "-c", line(c)} }, nil},
		{"env sh -c, not reviewed", false, func(c []string) []string {
			return []string{"env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x", "sh", "-c", line(c)}
		}, nil},
		{"timeout in a sh -c script, not reviewed", false, func(c []string) []string { return []string{"sh", "-c", "cd . && timeout 60 " + line(c)} }, nil},
		{"sh -c with a name and a path, not reviewed", false, func(c []string) []string { return []string{"sh", "-c", line(c), "probe", "/nonexistent/loop-probe-x"} }, nil},
		{"dash -c", false, func(c []string) []string { return []string{"dash", "-c", line(c)} }, nil},
		{"sh -c \"$@\" sh go", false, func(c []string) []string { return append([]string{"sh", "-c", `"$@"`, "sh"}, c...) }, nil},
		{"sh -c exec \"$@\" sh go", false, func(c []string) []string { return append([]string{"sh", "-c", `exec "$@"`, "sh"}, c...) }, nil},
		{"bash -c", false, func(c []string) []string { return []string{"bash", "-c", line(c)} }, nil},
		{"/bin/zsh -c", false, func(c []string) []string { return []string{"/bin/zsh", "-c", line(c)} }, nil},
		{"env dash -c", false, func(c []string) []string {
			return []string{"env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x", "dash", "-c", line(c)}
		}, nil},
		{"timeout bash -c", false, func(c []string) []string { return []string{"timeout", "900", "bash", "-c", line(c)} }, nil},
		{"sh -c \"$@\" with a name", false, func(c []string) []string { return append([]string{"sh", "-c", `"$@"`, "probe"}, c...) }, nil},
		{"sh -c $1 split into words", false, func(c []string) []string { return []string{"sh", "-c", "$1", "probe", line(c)} }, nil},
		{"sh -c \"$0\" -c", false, func(c []string) []string { return []string{"sh", "-c", `"$0" -c "$1"`, "sh", line(c)} }, nil},
		{"sh inside a sh -c script", false, func(c []string) []string { return []string{"sh", "-c", "sh -c '" + line(c) + "'"} }, nil},
		{"env sh -c inside a sh -c script", false, func(c []string) []string { return []string{"sh", "-c", "env X=1 sh -c '" + line(c) + "'"} }, nil},
		{"sh -ec", false, func(c []string) []string { return []string{"sh", "-ec", line(c)} }, nil},
		{"nice", false, func(c []string) []string { return append([]string{"nice"}, c...) }, nil},
		{"timeout nice", false, func(c []string) []string { return append([]string{"timeout", "900", "nice"}, c...) }, nil},
		{"timeout with options", false, func(c []string) []string { return append([]string{"timeout", "-k", "5", "900"}, c...) }, nil},
		{"env -S", false, func(c []string) []string { return []string{"env", "-S", line(c)} }, nil},
		{"xargs in a sh -c script", false, func(c []string) []string { return []string{"sh", "-c", "echo | xargs " + line(c)} }, nil},
		{"go run given go test", false, func(c []string) []string { return append([]string{"go", "run", "./scripts/probe"}, c...) }, nil},

		// Round 2 B1: a go that the shell expands.
		{"B1 ${GO:-go}", false, func(c []string) []string { return []string{"sh", "-c", "${GO:-go} " + rest(c)} }, nil},
		{"B1 env GO=go and $GO", false, func(c []string) []string { return []string{"env", "GO=go", "sh", "-c", "$GO " + rest(c)} }, nil},
		{"B1 a command in a script variable", false, func(c []string) []string { return []string{"sh", "-c", `X="` + line(c) + `"; $X`} }, nil},
		{"B1 a command in an env variable", false, func(c []string) []string { return []string{"env", "X=" + line(c), "sh", "-c", "$X"} }, nil},
		{"B1 a command in an env variable, eval", false, func(c []string) []string { return []string{"env", "X=" + line(c), "sh", "-c", `eval "$X"`} }, nil},
		{"B1 a glob that names go", false, func(c []string) []string { return []string{"sh", "-c", "/usr/local/bin/g[o] " + rest(c)} }, nil},
		{"B1 a positional fragment", false, func(c []string) []string { return []string{"sh", "-c", `"$1"o ` + rest(c), "probe", "g"} }, nil},

		// Round 3: argv[0] and env names off the allowlists, a reviewed script
		// with one byte changed.
		{"argv ${GO:-go}", false, func(c []string) []string { return append([]string{"${GO:-go}"}, c[1:]...) }, nil},
		{"argv $GO", false, func(c []string) []string { return append([]string{"$GO"}, c[1:]...) }, nil},
		{"argv path to go", false, func(c []string) []string { return append([]string{"/usr/local/go/bin/go"}, c[1:]...) }, nil},
		{"argv g[o]", false, func(c []string) []string { return append([]string{"g[o]"}, c[1:]...) }, nil},
		{"env GOFLAGS", false, func(c []string) []string { return append([]string{"env", "GOFLAGS=-count=1"}, c...) }, nil},
		{"env GO", false, func(c []string) []string { return append([]string{"env", "GO=go"}, c...) }, nil},
		{"timeout $T", false, func(c []string) []string { return append([]string{"timeout", "$T"}, c...) }, nil},
		{"reviewed script with one byte changed", false, func(c []string) []string { return []string{"sh", "-c", line(c) + " "} }, line},
	}
	for _, w := range wrappers {
		forms = append(forms, "wrapper: "+w.name)
		readable["wrapper: "+w.name] = w.readable
	}
	counts := []string{"", "-count=0", "-count=1"}
	cases := 0
	for _, tg := range targets {
		for _, form := range forms {
			for _, count := range counts {
				if tg.directory != "." && (form == "argv" || form == "env") {
					// These forms run in the root; the directory forms
					// cover another module.
					continue
				}
				cases++
				name := fmt.Sprintf("%s, %s, count %q", tg.name, form, count)
				// The oracle: -count=1 in an existing module never replays;
				// with no count only a plain argv in the root, of a package
				// the census resolves, is in a guarded class. A form the
				// guard cannot read is refused at every count.
				inCensus := tg.pkg == observed || tg.pkg == hermeticPackage || tg.pkg == k.module+"/internal/coverageprobe"
				want := count == "-count=1" && tg.module && readable[form] || count == "" && form == "argv" && tg.main && inCensus
				flags := []string{}
				if count != "" {
					flags = append(flags, count)
				}
				flags = append(flags, testTimeout)
				var argv []string
				switch form {
				case "argv", "env":
					argv = append(append([]string{"go", "test"}, flags...), tg.pkg)
					if form == "env" {
						argv = append([]string{"env", "ERGO_REPO_ROOT=/nonexistent/loop-probe-x"}, argv...)
					}
				case "go -C DIR test":
					argv = append(append([]string{"go", "-C", tg.directory, "test"}, flags...), tg.pkg)
				default:
					argv = append(append([]string{"go", "test", "-C", tg.directory}, flags...), tg.pkg)
				}
				mutated := slices.Clone(steps)
				census := k
				if tg.pkg == k.module+"/internal/coverageprobe" {
					census.packages = append(slices.Clone(k.packages), tg.pkg)
				}
				switch wrapperName, isWrapper := strings.CutPrefix(form, "wrapper: "); {
				case form == "reviewed script":
					text := "cd . && " + line(argv)
					census = census.withScripts(append(slices.Clone(k.scripts), probeScript(text, argv)))
					mutated = append(mutated, &Step{Name: "coverage probe", Argv: []string{"sh", "-c", text}, Env: tg.env})
				case strings.HasPrefix(form, "second go test in the provenance script"):
					changed := *provenance
					changed.Argv = slices.Clone(provenance.Argv)
					changed.Argv[2] = provenanceWith(argv)
					mutated[slices.Index(steps, provenance)] = &changed
					if form == "second go test in the provenance script, reviewed" {
						entry := *provenanceScript
						entry.text = changed.Argv[2]
						entry.runs = append([]scriptRun{{argv: argv}}, provenanceScript.runs...)
						scripts := slices.Clone(k.scripts)
						scripts[slices.Index(scripts, provenanceScript)] = &entry
						census = census.withScripts(scripts)
					}
				case form == "new step in shell, not reviewed":
					mutated = append(mutated, &Step{Name: "coverage probe", Argv: []string{"sh", "-c", "cd . && " + line(argv)}})
				case isWrapper:
					w := wrappers[slices.IndexFunc(wrappers, func(w wrapper) bool { return w.name == wrapperName })]
					if w.script != nil {
						census = census.withScripts(append(slices.Clone(k.scripts), probeScript(w.script(argv), argv)))
					}
					mutated = append(mutated, &Step{Name: "coverage probe", Argv: w.wrap(argv), Env: tg.env})
				default:
					mutated = append(mutated, &Step{Name: "coverage probe", Argv: argv, Env: tg.env})
				}
				refusals := census.refusals(mutated)
				if got := len(refusals) == 0; got != want {
					t.Errorf("%s: guard admits=%t, want %t: %q", name, got, want, refusals)
				}
			}
		}
	}

	// Steps the guard must refuse whatever they run: a script nobody
	// reviewed, with go or without it, the exact forms of rounds 1 and 2
	// (each a cached go test of tools/provenance), a program the guard does
	// not read and a variable a step may not set.
	b1 := "go test -C tools/provenance ./..."
	refused := []*Step{
		{Argv: []string{"dash", "/nonexistent/loop-probe-x.sh"}},
		{Argv: []string{"bash", "-c", "$ERGO_COVERAGE_PROBE"}},
		{Argv: []string{"timeout", "900", "zsh", "/nonexistent/loop-probe-x.sh"}},
		{Argv: []string{"sh", "/nonexistent/loop-probe-x.sh"}},
		{Argv: []string{"env", "-i", "make"}},
		{Argv: []string{"/nonexistent/loop-probe-x.sh"}},
		{Argv: []string{"make", "test"}},
		{Argv: []string{"sh", "-c", "true"}},
		{Argv: []string{"sh", "-c", "echo ok", "probe"}},
		{Argv: []string{"timeout", "900", "sh", "-c", b1}},
		{Argv: []string{"dash", "-c", b1}},
		{Argv: []string{"sh", "-c", `"$@"`, "sh", "go", "test", "-C", "tools/provenance", "./..."}},
		{Argv: []string{"sh", "-c", `exec "$@"`, "sh", "go", "test", "-C", "tools/provenance", "./..."}},
		{Argv: []string{"sh", "-c", "${GO:-go} -C tools/provenance test ./..."}},
		{Argv: []string{"env", "GO=go", "sh", "-c", "$GO -C tools/provenance test ./..."}},
		{Argv: []string{"sh", "-c", `X="` + b1 + `"; $X`}},
		{Argv: []string{"env", "X=" + b1, "sh", "-c", "$X"}},
		{Argv: []string{"env", "X=" + b1, "sh", "-c", `eval "$X"`}},
		{Argv: []string{"sh", "-c", "/usr/local/bin/g[o] -C tools/provenance test ./..."}},
		{Argv: []string{"sh", "-c", `"$1"o -C tools/provenance test ./...`, "probe", "g"}},
		{Argv: []string{"go", "generate", "./..."}},
		{Argv: []string{"go", "-C", "tools/provenance", "tool", "probe"}},
		{Argv: []string{"./bin/ergo", "run", "go"}},
		{Argv: []string{"go", "test", "-count=1", "./..."}, Env: []string{"GOFLAGS=-count=0"}},
		{Argv: []string{"go", "vet", "./..."}, Env: []string{"GO=go"}},
	}
	for _, step := range refused {
		step.Name = "coverage probe"
		if refusals := k.refusals([]*Step{step}); len(refusals) == 0 {
			t.Errorf("the guard admits a step that runs a script or command it cannot read: %q env %q", step.Argv, step.Env)
		}
	}

	// A reviewed script with one byte changed is a script nobody reviewed:
	// the guard refuses the gate's own sh -c steps with their first, middle
	// or last byte changed.
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
			if refusals := k.refusals([]*Step{&changed}); len(refusals) == 0 {
				t.Errorf("the guard admits step %q with byte %d of its script changed", step.Name, at)
			}
		}
	}
	if scriptSteps != len(reviewedScripts) {
		t.Errorf("the gate declares %d sh -c steps and %d reviewed scripts", scriptSteps, len(reviewedScripts))
	}

	// The gate's own shapes are read and admitted: each reviewed script with
	// its name and positional paths, and env with its assignment (the kernel
	// step). The test asserts their shape, so a refactor that drops the shape
	// does not leave the case vacuous.
	accepted := []*Step{stepNamed(t, steps, "no tracked python bytecode"), stepNamed(t, steps, "gofmt"), stepNamed(t, steps, "build bin/ergo"), provenance, kernel}
	for _, step := range accepted {
		if refusals := k.refusals([]*Step{step}); len(refusals) > 0 {
			t.Errorf("the guard refuses the gate's own shape %q: %q", step.Argv, refusals)
		}
	}
	for i, script := range []*reviewedScript{bytecodeScript, gofmtScript, stagedBuildScript, provenanceScript} {
		if argv := accepted[i].Argv; len(argv) < 3 || argv[0] != "sh" || argv[1] != "-c" || argv[2] != script.text {
			t.Errorf("step %q no longer runs the reviewed script %q: %q", accepted[i].Name, script.name, argv)
		}
	}
	if kernel.Argv[0] != "env" {
		t.Errorf("the kernel step is no longer behind env: %q", kernel.Argv)
	}

	// Mutations of the provenance script that its review would declare: the
	// guard reads the declared run as it reads a step's argv.
	for _, mutation := range []struct{ name, from, to string }{
		{"provenance without -count=1", "go test -C tools/provenance -count=1 ", "go test -C tools/provenance "},
		{"provenance with -count=0", "go test -C tools/provenance -count=1 ", "go test -C tools/provenance -count=0 "},
		{"provenance -C after a flag", "go test -C tools/provenance -count=1 ", "go test -count=1 -C tools/provenance "},
	} {
		if !strings.Contains(provenanceScript.text, mutation.from) {
			t.Fatalf("%s: the provenance script lacks %q", mutation.name, mutation.from)
		}
		entry := *provenanceScript
		entry.text = strings.Replace(provenanceScript.text, mutation.from, mutation.to, 1)
		entry.runs = slices.Clone(provenanceScript.runs)
		entry.runs[0] = scriptRun{argv: strings.Fields(strings.Replace(line(provenanceScript.runs[0].argv)+" ", mutation.from, mutation.to, 1))}
		scripts := slices.Clone(k.scripts)
		scripts[slices.Index(scripts, provenanceScript)] = &entry
		changed := *provenance
		changed.Argv = slices.Clone(provenance.Argv)
		changed.Argv[2] = entry.text
		mutated := slices.Clone(steps)
		mutated[slices.Index(steps, provenance)] = &changed
		if problems := declarationProblems(&entry); len(problems) > 0 {
			t.Fatalf("%s: the mutated entry's declaration does not match its text: %q", mutation.name, problems)
		}
		if len(k.withScripts(scripts).refusals(mutated)) == 0 {
			t.Errorf("%s: the guard admits %q", mutation.name, entry.text)
		}
		// Not reviewed, the same text is refused before its runs are read.
		if len(k.refusals(mutated)) == 0 {
			t.Errorf("%s: the guard admits the script without its review", mutation.name)
		}
	}
	for _, mutation := range []struct {
		name string
		edit func([]string) []string
	}{
		{"kernel without -count=1", func(argv []string) []string {
			return slices.DeleteFunc(argv, func(arg string) bool { return arg == "-count=1" })
		}},
		{"kernel with -count=0", func(argv []string) []string {
			argv[slices.Index(argv, "-count=1")] = "-count=0"
			return argv
		}},
	} {
		changed := *kernel
		changed.Argv = mutation.edit(slices.Clone(kernel.Argv))
		mutated := slices.Clone(steps)
		mutated[slices.Index(steps, kernel)] = &changed
		if len(k.refusals(mutated)) == 0 {
			t.Errorf("%s: the guard admits %q", mutation.name, changed.Argv)
		}
	}
	if cases < 500 {
		t.Fatalf("only %d generated cases; the property is vacuous", cases)
	}
}

func stepNamed(t *testing.T, steps []*Step, name string) *Step {
	t.Helper()
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("no step %q", name)
	return nil
}
