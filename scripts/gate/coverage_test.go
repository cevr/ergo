package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
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

// invocation is one go test command a step starts.
type invocation struct {
	step string
	// wrapped says how the command is written: "" for a plain step argv,
	// "env" behind an env prefix, "shell" inside a sh -c script.
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

// shellKeywords may start a shell command before the program it runs.
var shellKeywords = []string{"{", "}", "!", "if", "then", "else", "elif", "fi", "do", "done", "while", "until"}

// shells are programs that run a script or a command they are given. The
// guard reads only one shape of one shell, sh -c SCRIPT; every other shell, or
// sh in another shape, runs commands the guard cannot see.
var shells = []string{"sh", "bash", "dash", "zsh", "ksh", "mksh", "ash", "yash", "posh", "fish", "csh", "tcsh", "rc", "busybox"}

// stepInvocations returns every go test command step starts. The guard reads
// a step's argv through an allowlist of wrapper shapes and unwraps each to
// the command it runs (argvInvocations). A command it cannot read is an
// invocation with a problem, which the guard refuses.
func stepInvocations(step *Step) []invocation {
	return argvInvocations(step.Name, step.Argv, "")
}

// argvInvocations reads one command line. The wrapper shapes it understands
// are the ones the gate uses:
//
//   - go test ...: the invocation itself (parseGoCommand);
//   - env NAME=VALUE... COMMAND: COMMAND, behind env;
//   - timeout DURATION COMMAND: COMMAND, behind timeout;
//   - sh -c SCRIPT [NAME [ARG...]]: each simple command of SCRIPT, behind
//     shell. The guard does not read what SCRIPT does with NAME ($0) and the
//     ARGs ($1...): "$@" or "$0" -c may run them. So none of them may name
//     go or a shell. The gate's scripts pass a name and paths there (gofmt,
//     stagedBuild, provenanceAnalyzer), and their NAME is not "sh".
//
// Any other program, a go command other than go test included, is read as
// one that runs no go test of its own. If one of its arguments, split into
// shell words, names go or a shell, the guard cannot see what it runs, so it
// refuses the step. Every other shell, sh in another shape, and a wrapper
// with options are refused too: fail closed until classified.
func argvInvocations(step string, argv []string, wrapped string) []invocation {
	if len(argv) == 0 {
		return nil
	}
	refuse := func(problem string) []invocation {
		return []invocation{{step: step, wrapped: wrapped, problem: problem + ": " + strings.Join(argv, " ")}}
	}
	switch program := path.Base(argv[0]); program {
	case "go":
		if c, ok := parseGoCommand(step, argv, wrapped); ok {
			return []invocation{c}
		}
	case "env":
		rest := argv[1:]
		for len(rest) > 0 && isAssignment(rest[0]) {
			rest = rest[1:]
		}
		if len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			return refuse("env with options runs a command the guard cannot read")
		}
		return argvInvocations(step, rest, orDefault(wrapped, "env"))
	case "timeout":
		if len(argv) < 3 || strings.HasPrefix(argv[1], "-") {
			return refuse("timeout with options or without a command runs a command the guard cannot read")
		}
		return argvInvocations(step, argv[2:], orDefault(wrapped, "timeout"))
	case "sh":
		if len(argv) < 3 || argv[1] != "-c" {
			return refuse("a shell without -c runs a script the guard cannot read")
		}
		var found []invocation
		for _, words := range scriptCommands(argv[2]) {
			found = append(found, scriptInvocations(step, words)...)
		}
		if slices.ContainsFunc(argv[3:], namesCommand) {
			found = append(found, refuse("the arguments of a sh -c script name go or a shell, and the guard does not read how the script runs them")...)
		}
		return found
	default:
		if slices.Contains(shells, program) {
			return refuse("a shell other than sh -c runs a script the guard does not read")
		}
	}
	if slices.ContainsFunc(argv[1:], namesCommand) {
		return refuse("a program the guard does not read is given go or a shell")
	}
	return nil
}

// scriptInvocations reads one simple command of a shell script. The script's
// quotes are gone, so a shell inside it is refused: its own script cannot be
// told apart from its arguments.
func scriptInvocations(step string, words []string) []invocation {
	for len(words) > 0 && (isAssignment(words[0]) || slices.Contains(shellKeywords, words[0])) {
		words = words[1:]
	}
	if len(words) == 0 {
		return nil
	}
	if slices.ContainsFunc(words, isShell) {
		return []invocation{{step: step, wrapped: "shell", problem: "a shell inside a sh -c script runs commands the guard cannot read: " + strings.Join(words, " ")}}
	}
	if strings.Contains(words[0], "$") && len(words) > 1 && words[1] == "test" {
		return []invocation{{step: step, wrapped: "shell", problem: "a program named by a variable runs test: " + strings.Join(words, " ")}}
	}
	return argvInvocations(step, words, "shell")
}

func isGoProgram(word string) bool { return path.Base(word) == "go" }

func isShell(word string) bool { return slices.Contains(shells, path.Base(word)) }

// namesCommand reports whether an argument, split into shell words, names go
// or a shell.
func namesCommand(argument string) bool {
	for _, words := range scriptCommands(argument) {
		if slices.ContainsFunc(words, isGoProgram) || slices.ContainsFunc(words, isShell) {
			return true
		}
	}
	return false
}

func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
}

// scriptCommands splits a shell script into its simple commands, each a list
// of words. Every separator a command can hide behind ends a command, inside
// quotes too: a newline, ;, &, |, a parenthesis (also of $(...)) and a
// backquote. Quotes are dropped and a backslash-newline joins lines. The
// split is coarse on purpose: a quoted go test is read as a command, which
// can only refuse more.
func scriptCommands(script string) [][]string {
	var commands [][]string
	var words []string
	var word strings.Builder
	endWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			commands = append(commands, words)
			words = nil
		}
	}
	for i := 0; i < len(script); i++ {
		switch c := script[i]; c {
		case '\\':
			if i+1 < len(script) && script[i+1] == '\n' {
				i++
				endWord()
			}
		case '"', '\'':
		case ' ', '\t':
			endWord()
		case '\n', ';', '&', '|', '(', ')', '`':
			endCommand()
		default:
			word.WriteByte(c)
		}
	}
	endCommand()
	return commands
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
	if i >= len(argv) {
		return c, false
	}
	switch command := argv[i]; {
	case command == "test":
	case strings.HasPrefix(command, "-") || strings.Contains(command, "$"):
		c.problem = "a go command the guard cannot read: " + strings.Join(argv, " ")
		return c, true
	default:
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
// root, the domain internal/testinputs/guard walks.
type census struct {
	root, module string
	packages     []string
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
		for _, c := range stepInvocations(step) {
			if reason := k.refusal(c, step); reason != "" {
				found = append(found, c.String()+": "+reason)
			}
		}
	}
	return found
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
	return census{root: root, module: modulePath(root), packages: packages}, steps
}

// TestTestCommandsHaveCacheCoverage classifies every go test the gate
// declares, over the packages go list finds now, so a new package, shard or
// step is classified when it appears. Then it generates additions and
// mutations in each form a step can write a go test (a plain argv, go -C DIR
// test, go test -C DIR, an env prefix, a second command in the provenance
// script, and each wrapper the guard reads or must refuse), over each kind of
// target (an observed main-module package, a hermetic gate package, a
// package of another module, a missing module) and each count (none, 0, 1),
// and checks the guard's verdict against a table that does not parse
// commands.
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
		for _, c := range stepInvocations(step) {
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
	controls := stepInvocations(provenance)
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
	forms := []string{"argv", "go -C DIR test", "go test -C DIR", "env", "shell", "new step in shell"}
	// Wrappers run the go test -C DIR form. The guard can unwrap a readable
	// one, so it admits the command exactly where it admits the bare -C form
	// with -count=1. Every other wrapper hides the command, so the guard
	// refuses it at every count. The first four unreadable ones are the
	// forms gate-testcache-gap round 1 found admitted (M1).
	type wrapper struct {
		name     string
		readable bool
		wrap     func(command []string) []string
	}
	line := func(command []string) string { return strings.Join(command, " ") }
	wrappers := []wrapper{
		{"timeout", true, func(c []string) []string { return append([]string{"timeout", "900"}, c...) }},
		{"timeout sh -c", true, func(c []string) []string { return []string{"timeout", "900", "sh", "-c", line(c)} }},
		{"env sh -c", true, func(c []string) []string { return []string{"env", "ERGO_COVERAGE_PROBE=1", "sh", "-c", line(c)} }},
		{"timeout in a sh -c script", true, func(c []string) []string { return []string{"sh", "-c", "cd . && timeout 60 " + line(c)} }},
		{"nested wrappers", true, func(c []string) []string {
			return []string{"timeout", "900", "env", "ERGO_COVERAGE_PROBE=1", "timeout", "60", "sh", "-c", "cd . && " + line(c)}
		}},
		{"sh -c with a name and a path", true, func(c []string) []string { return []string{"sh", "-c", line(c), "probe", "/nonexistent/loop-probe-x"} }},

		{"dash -c", false, func(c []string) []string { return []string{"dash", "-c", line(c)} }},
		{"sh -c \"$@\" sh go", false, func(c []string) []string { return append([]string{"sh", "-c", `"$@"`, "sh"}, c...) }},
		{"sh -c exec \"$@\" sh go", false, func(c []string) []string { return append([]string{"sh", "-c", `exec "$@"`, "sh"}, c...) }},
		{"bash -c", false, func(c []string) []string { return []string{"bash", "-c", line(c)} }},
		{"/bin/zsh -c", false, func(c []string) []string { return []string{"/bin/zsh", "-c", line(c)} }},
		{"env dash -c", false, func(c []string) []string { return []string{"env", "ERGO_COVERAGE_PROBE=1", "dash", "-c", line(c)} }},
		{"timeout bash -c", false, func(c []string) []string { return []string{"timeout", "900", "bash", "-c", line(c)} }},
		{"sh -c \"$@\" with a name", false, func(c []string) []string { return append([]string{"sh", "-c", `"$@"`, "probe"}, c...) }},
		{"sh -c $1 split into words", false, func(c []string) []string { return []string{"sh", "-c", "$1", "probe", line(c)} }},
		{"sh -c \"$0\" -c", false, func(c []string) []string { return []string{"sh", "-c", `"$0" -c "$1"`, "sh", line(c)} }},
		{"sh inside a sh -c script", false, func(c []string) []string { return []string{"sh", "-c", "sh -c '" + line(c) + "'"} }},
		{"env sh -c inside a sh -c script", false, func(c []string) []string { return []string{"sh", "-c", "env X=1 sh -c '" + line(c) + "'"} }},
		{"sh -ec", false, func(c []string) []string { return []string{"sh", "-ec", line(c)} }},
		{"nice", false, func(c []string) []string { return append([]string{"nice"}, c...) }},
		{"timeout nice", false, func(c []string) []string { return append([]string{"timeout", "900", "nice"}, c...) }},
		{"timeout with options", false, func(c []string) []string { return append([]string{"timeout", "-k", "5", "900"}, c...) }},
		{"env -S", false, func(c []string) []string { return []string{"env", "-S", line(c)} }},
		{"xargs in a sh -c script", false, func(c []string) []string { return []string{"sh", "-c", "echo | xargs " + line(c)} }},
		{"go run given go test", false, func(c []string) []string { return append([]string{"go", "run", "./scripts/probe"}, c...) }},
	}
	for _, w := range wrappers {
		forms = append(forms, "wrapper: "+w.name)
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
				wrapperName, isWrapper := strings.CutPrefix(form, "wrapper: ")
				w := wrappers[slices.IndexFunc(wrappers, func(w wrapper) bool { return !isWrapper || w.name == wrapperName })]
				// The oracle: -count=1 in an existing module never replays;
				// with no count only a plain argv in the root, of a package
				// the census resolves, is in a guarded class. A wrapper the
				// guard cannot read is refused at every count.
				inCensus := tg.pkg == observed || tg.pkg == hermeticPackage || tg.pkg == k.module+"/internal/coverageprobe"
				want := count == "-count=1" && tg.module && (!isWrapper || w.readable) || count == "" && form == "argv" && tg.main && inCensus
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
						argv = append([]string{"env", "ERGO_COVERAGE_PROBE=1"}, argv...)
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
				switch {
				case form == "shell":
					// A second go test in the real provenance script.
					changed := *provenance
					changed.Argv = slices.Clone(provenance.Argv)
					changed.Argv[2] = strings.Replace(changed.Argv[2], "&&\n", "&&\n"+strings.Join(argv, " ")+" &&\n", 1)
					mutated[slices.Index(steps, provenance)] = &changed
				case form == "new step in shell":
					mutated = append(mutated, &Step{Name: "coverage probe", Argv: []string{"sh", "-c", "cd . && " + strings.Join(argv, " ")}})
				case isWrapper:
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

	// A step that runs a script the guard cannot read is refused even when
	// no go appears in its argv.
	for _, argv := range [][]string{
		{"dash", "/nonexistent/loop-probe-x.sh"},
		{"bash", "-c", "$ERGO_COVERAGE_PROBE"},
		{"timeout", "900", "zsh", "/nonexistent/loop-probe-x.sh"},
		{"sh", "/nonexistent/loop-probe-x.sh"},
		{"env", "-i", "make"},
	} {
		if refusals := k.refusals([]*Step{{Name: "coverage probe", Argv: argv}}); len(refusals) == 0 {
			t.Errorf("the guard admits a step that runs a script or command it cannot read: %q", argv)
		}
	}

	// The gate's own wrapper shapes are read and admitted: a sh -c script
	// with a name and positional paths (gofmt, stagedBuild, provenance) and
	// env with assignments (the kernel step). The test asserts their shape,
	// so a refactor that drops the shape does not leave the case vacuous.
	accepted := []*Step{stepNamed(t, steps, "gofmt"), stepNamed(t, steps, "build bin/ergo"), provenance, kernel}
	for _, step := range accepted {
		if refusals := k.refusals([]*Step{step}); len(refusals) > 0 {
			t.Errorf("the guard refuses the gate's own shape %q: %q", step.Argv, refusals)
		}
	}
	for _, step := range accepted[:3] {
		if len(step.Argv) < 4 || step.Argv[0] != "sh" || step.Argv[1] != "-c" {
			t.Errorf("step %q is no longer a sh -c script with positional arguments: %q", step.Name, step.Argv)
		}
	}
	if kernel.Argv[0] != "env" {
		t.Errorf("the kernel step is no longer behind env: %q", kernel.Argv)
	}

	// Mutations of the real step bodies.
	for _, mutation := range []struct{ name, from, to string }{
		{"provenance without -count=1", "go test -C tools/provenance -count=1 ", "go test -C tools/provenance "},
		{"provenance with -count=0", "go test -C tools/provenance -count=1 ", "go test -C tools/provenance -count=0 "},
		{"provenance -C after a flag", "go test -C tools/provenance -count=1 ", "go test -count=1 -C tools/provenance "},
		{"provenance controls through a variable", "go test -C tools/provenance -count=1 ", "$GO test -C tools/provenance "},
		{"provenance controls through xargs", "go test -C tools/provenance -count=1 ", "echo | xargs go test -C tools/provenance "},
	} {
		changed := *provenance
		changed.Argv = slices.Clone(provenance.Argv)
		if !strings.Contains(changed.Argv[2], mutation.from) {
			t.Fatalf("%s: the provenance script lacks %q", mutation.name, mutation.from)
		}
		changed.Argv[2] = strings.Replace(changed.Argv[2], mutation.from, mutation.to, 1)
		mutated := slices.Clone(steps)
		mutated[slices.Index(steps, provenance)] = &changed
		if len(k.refusals(mutated)) == 0 {
			t.Errorf("%s: the guard admits %q", mutation.name, changed.Argv[2])
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
