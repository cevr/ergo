// Command examples builds each example program under examples/ with the
// checkout's ergo and compares what it prints with its recorded cases: stdout,
// stderr and the exit status, byte for byte. examples/README.md describes the
// layout, the case format, the tags and the record leg.
//
//	go run ./scripts/examples [flags] [PROGRAM...]
//
// It imports the standard library, internal/proclimit for the memory limit
// of the programs it runs, and, for the law check (laws.go), the checker's
// front end: the gate needs only Go and Git.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// knownTags are the words a program's tags file may hold. default stands
// alone; the others keep a program out of the default gate.
var knownTags = []string{"default", "slow", "net", "cc", "tty", "gui"}

// laterLegs are the tags whose legs are later units: selecting such a
// program is a diagnostic, never a silent run.
var laterLegs = map[string]string{
	"tty": "a pseudo-terminal, unit G9",
	"gui": "a window, unit R6",
}

// nonPrograms are the folders of examples/ that are not harness programs.
// Every other folder must be a program.
var nonPrograms = map[string]string{
	// editions/sketch is pinned by cmd/ergo's and the sketch packages' Go
	// tests and by README.md; the unit that retires those tests adopts it.
	"editions": "edition fixtures that Go tests pin",
	// go-interop is a Go program: go vet and go test build check it.
	"go-interop": "a Go program the Go steps build",
}

// caseFiles are the files a case folder may hold.
var caseFiles = []string{"args", "stdin", "env", "setup.sh", "expected.out", "expected.err", "expected.status"}

// testDeclaration finds a top-level test declaration in an .ergo file.
var testDeclaration = regexp.MustCompile(`(?m)^test [A-Za-z_]`)

type config struct {
	root    string
	ergo    string
	tags    []string
	timeout time.Duration
	budget  time.Duration
	record  bool
	check   bool
	lawless bool
	names   []string
	// memory is the limit of each built program a case runs: ERGO_PROGRAM_MEMORY,
	// 2 GiB by default (internal/proclimit; docs/tooling.md, "Test memory").
	// `ergo test` reads the same variable for the test binaries it runs.
	memory uint64
}

type program struct {
	name    string
	dir     string
	tags    []string
	sources []string // the .ergo files the fmt leg checks, sorted
	port    bool     // holds record.sh and origin
	tested  bool     // declares a test
	// module marks a program of packages (196b): ergo.mod, the binary's
	// package cmd/NAME/main.ergo and package folders. main is what the
	// build leg builds, and tests are the targets of the test leg: one
	// per package that declares a test, sorted.
	module bool
	main   string
	tests  []string
	cases  []*testCase
}

func (p *program) tagged(tag string) bool { return slices.Contains(p.tags, tag) }

type testCase struct {
	name   string
	dir    string
	args   []string
	stdin  string // the stdin file, or "" for empty input
	env    []string
	setup  bool
	out    []byte
	hasOut bool
	err    []byte
	status int
}

// usageError is a bad flag or name: exit 2.
type usageError string

func (u usageError) Error() string { return string(u) }

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "examples:", err)
		}
		return 2
	}
	if cfg.lawless {
		return reportLawless(cfg.root, stdout)
	}
	programs, problems := loadExamples(cfg.root, cfg.record)
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintln(stderr, problem)
		}
		fmt.Fprintf(stderr, "examples: %d layout errors; see examples/README.md\n", len(problems))
		return 2
	}
	selected, err := selectPrograms(cfg, programs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(selected) == 0 {
		fmt.Fprintf(stdout, "examples: no program selected (-tags %s); nothing to run\n", strings.Join(cfg.tags, ","))
		return 0
	}
	// From here every exit path the runner controls stops each process group
	// it started: the end of a command, a timeout, the cap, a panic on this
	// goroutine (execute's deferred stop) and a stop signal.
	l := newLive()
	defer l.handleSignals(stderr)()
	scratch, err := l.makeDir("", "ergo-examples-")
	if err != nil {
		fmt.Fprintln(stderr, "examples:", err)
		return 2
	}
	defer l.removeDir(scratch)
	r := &runner{cfg: cfg, out: stdout, scratch: scratch, live: l}
	for _, p := range selected {
		if cfg.record {
			r.recordProgram(p)
		} else {
			r.runProgram(p)
		}
	}
	return r.finish()
}

func parseFlags(args []string, stderr io.Writer) (*config, error) {
	flags := flag.NewFlagSet("examples", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cfg := &config{}
	var tags string
	flags.StringVar(&cfg.root, "root", ".", "repository root; programs are the folders `DIR`/examples/*")
	flags.StringVar(&cfg.ergo, "ergo", "bin/ergo", "the compiler; a relative `PATH` is relative to -root")
	flags.StringVar(&tags, "tags", "default", "comma-separated tags; selects the programs whose tags hold any of them")
	flags.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "per case (and per ergo test); on expiry the case fails and its process group is stopped")
	flags.DurationVar(&cfg.budget, "budget", 5*time.Second, "a case of a default program that runs longer fails; tag the program slow")
	flags.BoolVar(&cfg.record, "record", false, "run each selected port's record.sh in place of the built program and write its goldens")
	flags.BoolVar(&cfg.check, "check", false, "with -record: compare the original's output with the goldens; write nothing")
	flags.BoolVar(&cfg.lawless, "lawless", false, "report every operation type under examples/ with no law, run nothing, and exit 0")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: go run ./scripts/examples [flags] [PROGRAM...]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if cfg.check && !cfg.record {
		return nil, usageError("-check needs -record")
	}
	if cfg.timeout <= 0 {
		return nil, usageError("-timeout must be positive")
	}
	memory, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return nil, usageError(err.Error())
	}
	cfg.memory = memory
	root, err := filepath.Abs(cfg.root)
	if err != nil {
		return nil, err
	}
	cfg.root = root
	if !filepath.IsAbs(cfg.ergo) {
		cfg.ergo = filepath.Join(root, cfg.ergo)
	}
	for _, tag := range strings.Split(tags, ",") {
		tag = strings.TrimSpace(tag)
		if !slices.Contains(knownTags, tag) {
			return nil, usageError(fmt.Sprintf("-tags: unknown tag %q (known: %s)", tag, strings.Join(knownTags, " ")))
		}
		if !slices.Contains(cfg.tags, tag) {
			cfg.tags = append(cfg.tags, tag)
		}
	}
	cfg.names = flags.Args()
	return cfg, nil
}

// loadExamples reads and checks the layout of every folder of examples/,
// whatever the selection. Each problem names its folder.
func loadExamples(root string, record bool) ([]*program, []string) {
	examples := filepath.Join(root, "examples")
	entries, err := os.ReadDir(examples)
	if err != nil {
		return nil, []string{fmt.Sprintf("examples: %v", err)}
	}
	var programs []*program
	var problems []string
	for _, entry := range entries {
		name := entry.Name()
		label := "examples/" + name
		if !entry.IsDir() {
			// A file beside the programs: only the folder's README.
			if name == "README.md" && entry.Type().IsRegular() {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s: not a program folder; examples/ holds program folders and README.md", label))
			continue
		}
		if _, ok := nonPrograms[name]; ok {
			continue
		}
		p, found := loadProgram(filepath.Join(examples, name), name, record)
		for _, problem := range found {
			problems = append(problems, label+": "+problem)
		}
		if len(found) == 0 {
			programs = append(programs, p)
		}
	}
	return programs, problems
}

func loadProgram(dir, name string, record bool) (*program, []string) {
	p := &program{name: name, dir: dir}
	var problems []string
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	present := map[string]bool{}
	for _, entry := range entries {
		if entry.Name() == "ergo.mod" {
			p.module = true
		}
	}
	if p.module {
		return loadModuleProgram(p, entries, record)
	}
	for _, entry := range entries {
		file := entry.Name()
		present[file] = true
		switch {
		case file == "cases" || file == "testdata":
			if !entry.IsDir() {
				problem("%s is not a folder", file)
			}
		case file == "main.ergo" || file == "README.md" || file == "tags" || file == "origin" || file == "record.sh" || strings.HasSuffix(file, ".ergo"):
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
				continue
			}
			if strings.HasSuffix(file, ".ergo") {
				p.sources = append(p.sources, filepath.Join(dir, file))
			}
		default:
			problem("unexpected %s; a program folder holds main.ergo, README.md, tags and cases/, and may hold origin with record.sh, testdata/ and other .ergo files", file)
		}
	}
	if !present["main.ergo"] {
		problem("not a program: no main.ergo (the folders that are not programs are %s)", strings.Join(sortedKeys(nonPrograms), " and "))
		return nil, problems
	}
	p.main = filepath.Join(dir, "main.ergo")
	return finishProgram(p, present, problems, record)
}

// loadModuleProgram reads the layout of a program of packages (196b; the
// Go layout of AGENTS.md): ergo.mod, the binary's package cmd/NAME (NAME
// being the program folder's name) with its main.ergo, and package
// folders of .ergo files, beside the files every program holds. Top-level
// .ergo files are the module root's package.
func loadModuleProgram(p *program, entries []os.DirEntry, record bool) (*program, []string) {
	var problems []string
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	present := map[string]bool{}
	for _, entry := range entries {
		file := entry.Name()
		present[file] = true
		switch {
		case file == "cases" || file == "testdata":
			if !entry.IsDir() {
				problem("%s is not a folder", file)
			}
		case file == "ergo.mod" || file == "README.md" || file == "tags" || file == "origin" || file == "record.sh" || strings.HasSuffix(file, ".ergo"):
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
				continue
			}
			if strings.HasSuffix(file, ".ergo") {
				p.sources = append(p.sources, filepath.Join(p.dir, file))
			}
		case entry.IsDir():
			if file == "cmd" {
				problems = append(problems, checkCommands(p, filepath.Join(p.dir, file))...)
			}
			problems = append(problems, packageSources(p, filepath.Join(p.dir, file), file)...)
		default:
			problem("unexpected %s; a program of packages holds ergo.mod, cmd/%s/main.ergo, package folders, README.md, tags and cases/, and may hold origin with record.sh and testdata/", file, p.name)
		}
	}
	if !present["cmd"] {
		problem("a program of packages has no cmd/%s/main.ergo", p.name)
	}
	slices.Sort(p.sources)
	problems = append(problems, moduleLawProblems(p)...)
	p.main = filepath.Join(p.dir, "cmd", p.name)
	return finishProgram(p, present, problems, record)
}

// checkCommands checks that cmd holds the one binary of the program,
// cmd/NAME with its main.ergo.
func checkCommands(p *program, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	if len(entries) != 1 || entries[0].Name() != p.name || !entries[0].IsDir() {
		return []string{fmt.Sprintf("cmd holds one folder, cmd/%s, the program's binary", p.name)}
	}
	if info, err := os.Stat(filepath.Join(dir, p.name, "main.ergo")); err != nil || !info.Mode().IsRegular() {
		return []string{fmt.Sprintf("no cmd/%s/main.ergo", p.name)}
	}
	return nil
}

// packageSources adds the .ergo files of the package folder dir, and of
// the package folders under it, to the program's sources. A package
// folder holds .ergo files and package folders only.
func packageSources(p *program, dir, label string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			problems = append(problems, packageSources(p, filepath.Join(dir, name), label+"/"+name)...)
		case strings.HasSuffix(name, ".ergo") && entry.Type().IsRegular():
			p.sources = append(p.sources, filepath.Join(dir, name))
		default:
			problems = append(problems, fmt.Sprintf("unexpected %s/%s; a package folder holds .ergo files and package folders", label, name))
		}
	}
	return problems
}

// finishProgram checks the files every program holds, reads its tags,
// finds its tests and loads its cases.
func finishProgram(p *program, present map[string]bool, problems []string, record bool) (*program, []string) {
	dir := p.dir
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, required := range []string{"README.md", "tags", "cases"} {
		if !present[required] {
			problem("no %s", required)
		}
	}
	if present["origin"] != present["record.sh"] {
		problem("origin and record.sh go together: a port names its original in origin and runs it in record.sh")
	}
	p.port = present["record.sh"]
	if present["tags"] {
		tags, err := readTags(filepath.Join(dir, "tags"))
		if err != nil {
			problem("tags: %v", err)
		}
		p.tags = tags
	}
	for _, source := range p.sources {
		content, err := os.ReadFile(source)
		if err != nil {
			problem("%v", err)
			continue
		}
		if !testDeclaration.Match(content) {
			continue
		}
		p.tested = true
		// A program of one folder tests its main.ergo, as before; a module
		// program tests each package folder that declares a test.
		target := p.main
		if p.module {
			target = filepath.Dir(source)
		}
		if !slices.Contains(p.tests, target) {
			p.tests = append(p.tests, target)
		}
	}
	slices.Sort(p.tests)
	if present["cases"] {
		cases, found := loadCases(filepath.Join(dir, "cases"), record)
		problems = append(problems, found...)
		p.cases = cases
	}
	return p, problems
}

func readTags(file string) ([]string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	line := strings.TrimSuffix(string(content), "\n")
	if strings.ContainsAny(line, "\n\r\t") || line != strings.TrimSpace(line) || strings.Contains(line, "  ") {
		return nil, errors.New("want one line of words separated by single spaces")
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return nil, fmt.Errorf("empty; want words from: %s", strings.Join(knownTags, " "))
	}
	var tags []string
	for _, word := range words {
		if !slices.Contains(knownTags, word) {
			return nil, fmt.Errorf("unknown tag %q; known: %s", word, strings.Join(knownTags, " "))
		}
		if slices.Contains(tags, word) {
			return nil, fmt.Errorf("tag %q twice", word)
		}
		tags = append(tags, word)
	}
	if slices.Contains(tags, "default") && len(tags) > 1 {
		return nil, fmt.Errorf("default stands alone, got %q", line)
	}
	return tags, nil
}

func loadCases(dir string, record bool) ([]*testCase, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var cases []*testCase
	var problems []string
	for _, entry := range entries {
		label := "cases/" + entry.Name()
		if !entry.IsDir() {
			problems = append(problems, label+": not a case folder")
			continue
		}
		c, found := loadCase(filepath.Join(dir, entry.Name()), entry.Name(), record)
		for _, problem := range found {
			problems = append(problems, label+": "+problem)
		}
		if len(found) == 0 {
			cases = append(cases, c)
		}
	}
	if len(entries) == 0 {
		problems = append(problems, "cases/ holds no case")
	}
	return cases, problems
}

func loadCase(dir, name string, record bool) (*testCase, []string) {
	c := &testCase{name: name, dir: dir}
	var problems []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	present := map[string]bool{}
	for _, entry := range entries {
		file := entry.Name()
		if !slices.Contains(caseFiles, file) {
			problems = append(problems, fmt.Sprintf("unexpected %s; a case holds expected.out and may hold %s", file, strings.Join(slices.DeleteFunc(slices.Clone(caseFiles), func(f string) bool { return f == "expected.out" }), ", ")))
			continue
		}
		if !entry.Type().IsRegular() {
			problems = append(problems, file+" is not a regular file")
			continue
		}
		present[file] = true
	}
	read := func(file string) []byte {
		if !present[file] {
			return nil
		}
		content, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			problems = append(problems, err.Error())
		}
		return content
	}
	if !present["expected.out"] && !record {
		problems = append(problems, "no expected.out")
	}
	c.out, c.hasOut = read("expected.out"), present["expected.out"]
	c.err = read("expected.err")
	c.args = lines(read("args"))
	for _, line := range lines(read("env")) {
		if key, _, ok := strings.Cut(line, "="); !ok || key == "" {
			problems = append(problems, fmt.Sprintf("env: line %q is not KEY=VALUE", line))
		}
		c.env = append(c.env, line)
	}
	if present["expected.status"] {
		text := strings.TrimSuffix(string(read("expected.status")), "\n")
		status, err := strconv.Atoi(text)
		if err != nil || status < 0 || status > 255 || strconv.Itoa(status) != text {
			problems = append(problems, fmt.Sprintf("expected.status: %q is not an exit status (decimal 0 to 255)", text))
		}
		c.status = status
	}
	if present["stdin"] {
		c.stdin = filepath.Join(dir, "stdin")
	}
	c.setup = present["setup.sh"]
	return c, problems
}

// lines splits a file into its lines: the final newline ends the last line,
// an empty line is an empty string, and an empty file has no lines.
func lines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
}

func selectPrograms(cfg *config, programs []*program) ([]*program, error) {
	var selected []*program
	if len(cfg.names) > 0 {
		for _, name := range cfg.names {
			at := slices.IndexFunc(programs, func(p *program) bool { return p.name == name })
			if at < 0 {
				return nil, usageError(fmt.Sprintf("examples: unknown program %q", name))
			}
			if !slices.Contains(selected, programs[at]) {
				selected = append(selected, programs[at])
			}
		}
		slices.SortFunc(selected, func(a, b *program) int { return strings.Compare(a.name, b.name) })
	} else {
		for _, p := range programs {
			if slices.ContainsFunc(cfg.tags, p.tagged) {
				selected = append(selected, p)
			}
		}
	}
	for _, p := range selected {
		for _, tag := range p.tags {
			if leg, later := laterLegs[tag]; later {
				return nil, usageError(fmt.Sprintf("examples/%s: tagged %s; its leg (%s) is not built yet, so it cannot run", p.name, tag, leg))
			}
		}
		if cfg.record && !p.port {
			return nil, usageError(fmt.Sprintf("examples/%s: no record.sh; -record runs a port's original, and only a port has one", p.name))
		}
	}
	return selected, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

type runner struct {
	cfg      *config
	out      io.Writer
	scratch  string // holds only the built programs
	live     *live
	cases    int
	failures []string
}

func (r *runner) fail(what string) { r.failures = append(r.failures, what) }

func (r *runner) finish() int {
	if len(r.failures) > 0 {
		fmt.Fprintf(r.out, "examples: FAILED: %s\n", strings.Join(r.failures, ", "))
		return 1
	}
	verb := "passed"
	if r.cfg.record && !r.cfg.check {
		verb = "recorded"
	}
	fmt.Fprintf(r.out, "examples: %s; cases: %d\n", verb, r.cases)
	return 0
}

// tool runs a compiler command from the root with the runner's environment
// and empty input, in its own process group.
func (r *runner) tool(timeout time.Duration, args ...string) (outcome, error) {
	return r.live.execute(append([]string{r.cfg.ergo}, args...), r.cfg.root, os.Environ(), "", timeout, 0)
}

// toolFailed prints the failure of a compiler leg (fmt or build) and counts
// it. Output over the cap is named, never printed.
func (r *runner) toolFailed(p *program, leg string, o outcome, err error) {
	if over := o.overCap(); err == nil && over != "" {
		fmt.Fprintf(r.out, "FAIL %s: %s: %s\n", p.name, leg, over)
	} else {
		fmt.Fprintf(r.out, "FAIL %s: %s\n", p.name, leg)
		r.printOutput(o, err)
	}
	r.fail(p.name + ": " + leg)
}

// runProgram formats, builds and tests one program and runs its cases.
func (r *runner) runProgram(p *program) {
	if o, err := r.tool(0, append([]string{"fmt", "--check"}, p.sources...)...); err != nil || !o.passed() {
		r.toolFailed(p, "fmt", o, err)
		return
	}
	dir, err := os.MkdirTemp(r.scratch, "build-")
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s: build: %v\n", p.name, err)
		r.fail(p.name + ": build")
		return
	}
	binary := filepath.Join(dir, p.name)
	if o, err := r.tool(0, "build", p.main, "-o", binary); err != nil || !o.passed() {
		r.toolFailed(p, "build", o, err)
		return
	}
	if p.tested {
		r.testProgram(p)
	}
	for _, c := range p.cases {
		r.runCase(p, c, []string{binary}, true)
	}
}

// testProgram runs the program's own test declarations: `ergo test` of its
// main.ergo, or of each package folder of a program of packages that
// declares a test (as go test runs one package at a time). A failure fails
// the program; its cases still run.
func (r *runner) testProgram(p *program) {
	for _, target := range p.tests {
		leg := "test"
		if p.module {
			rel, _ := filepath.Rel(p.dir, target)
			leg = "test " + filepath.ToSlash(rel)
		}
		r.testTarget(p, target, leg)
	}
}

func (r *runner) testTarget(p *program, target, leg string) {
	o, err := r.tool(r.cfg.timeout, "test", target)
	switch {
	case err != nil:
		fmt.Fprintf(r.out, "FAIL %s: %s: could not start: %v\n", p.name, leg, err)
		r.fail(p.name + ": " + leg)
	case o.overCap() != "":
		fmt.Fprintf(r.out, "FAIL %s: %s: %s\n", p.name, leg, o.overCap())
		r.fail(p.name + ": " + leg)
	case o.passed():
		fmt.Fprintf(r.out, "ok %s: %s (%s)\n", p.name, leg, seconds(o.elapsed))
	default:
		fmt.Fprintf(r.out, "FAIL %s: %s%s\n", p.name, leg, o.why(r.cfg.timeout))
		r.printOutput(o, nil)
		r.fail(p.name + ": " + leg)
	}
}

// recordProgram runs a port's original through record.sh for each case and
// writes its goldens, or with -check compares them.
func (r *runner) recordProgram(p *program) {
	for _, c := range p.cases {
		command := []string{"sh", filepath.Join(p.dir, "record.sh")}
		if r.cfg.check {
			r.runCase(p, c, command, false)
			continue
		}
		// The goldens come only from a run of the original that started,
		// stayed within the cap and ended by itself: execCase fails a case
		// whose command could not start, and output over the cap, a timeout
		// or a signal records nothing.
		o, ok := r.execCase(p, c, command, 0)
		if !ok {
			continue
		}
		if over := o.overCap(); over != "" {
			fmt.Fprintf(r.out, "FAIL %s/%s: %s\n", p.name, c.name, over)
			r.fail(p.name + "/" + c.name)
			continue
		}
		if o.timedOut || o.status < 0 {
			fmt.Fprintf(r.out, "FAIL %s/%s:%s\n", p.name, c.name, o.why(r.cfg.timeout))
			r.fail(p.name + "/" + c.name)
			continue
		}
		if err := writeGoldens(c.dir, o); err != nil {
			fmt.Fprintf(r.out, "FAIL %s/%s: write goldens: %v\n", p.name, c.name, err)
			r.fail(p.name + "/" + c.name)
			continue
		}
		r.cases++
		fmt.Fprintf(r.out, "recorded %s/%s\n", p.name, c.name)
	}
}

// writeGoldens writes expected.out always, expected.err only when stderr is
// not empty and expected.status only when the status is not 0, removing a
// stale one otherwise.
func writeGoldens(dir string, o outcome) error {
	if err := os.WriteFile(filepath.Join(dir, "expected.out"), o.stdout, 0o644); err != nil {
		return err
	}
	optional := func(file string, content []byte) error {
		name := filepath.Join(dir, file)
		if content == nil {
			if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}
		return os.WriteFile(name, content, 0o644)
	}
	var stderr, status []byte
	if len(o.stderr) > 0 {
		stderr = o.stderr
	}
	if o.status != 0 {
		status = []byte(strconv.Itoa(o.status) + "\n")
	}
	if err := optional("expected.err", stderr); err != nil {
		return err
	}
	return optional("expected.status", status)
}

// execCase runs one case's setup.sh and command in a fresh empty directory.
// It prints a FAIL line and returns false when the case could not run: its
// directory, its setup.sh or its command could not start, or setup.sh failed.
// So an outcome it returns is always of a command that started. The command
// runs under the memory limit memory (0: none).
func (r *runner) execCase(p *program, c *testCase, command []string, memory uint64) (outcome, bool) {
	work, err := r.live.makeDir("", "ergo-case-")
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s/%s: make its directory: %v\n", p.name, c.name, err)
		r.fail(p.name + "/" + c.name)
		return outcome{}, false
	}
	defer r.live.removeDir(work)
	env := []string{"HOME=" + work}
	if path, ok := os.LookupEnv("PATH"); ok {
		env = append([]string{"PATH=" + path}, env...)
	}
	env = append(env, c.env...)
	if c.setup {
		o, err := r.live.execute([]string{"sh", filepath.Join(c.dir, "setup.sh"), c.dir}, work, env, "", r.cfg.timeout, 0)
		if err != nil || !o.passed() {
			what := fmt.Sprintf("setup.sh exit %d", o.status)
			switch {
			case err != nil:
				what = fmt.Sprintf("setup.sh could not start: %v", err)
			case o.overCap() != "":
				what = "setup.sh " + o.overCap()
			case o.timedOut || o.status < 0:
				what = "setup.sh" + o.why(r.cfg.timeout)
			}
			fmt.Fprintf(r.out, "FAIL %s/%s: %s\n", p.name, c.name, what)
			if o.overCap() == "" {
				r.printOutput(o, nil)
			}
			r.fail(p.name + "/" + c.name)
			return outcome{}, false
		}
	}
	o, err := r.live.execute(append(slices.Clone(command), c.args...), work, env, c.stdin, r.cfg.timeout, memory)
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s/%s: could not start: %v\n", p.name, c.name, err)
		r.fail(p.name + "/" + c.name)
		return outcome{}, false
	}
	return o, true
}

// runCase runs one case and compares its streams and status with the goldens.
// budgeted marks the built program, which also runs under the memory limit;
// a port's original (record.sh) runs under neither.
func (r *runner) runCase(p *program, c *testCase, command []string, budgeted bool) {
	var memory uint64
	if budgeted {
		memory = r.cfg.memory
	}
	o, ok := r.execCase(p, c, command, memory)
	if !ok {
		return
	}
	r.cases++
	label := p.name + "/" + c.name
	if o.limited {
		fmt.Fprintf(r.out, "FAIL %s: program %s exceeded its memory limit of %s (set %s to change it, 0 for none)\n",
			label, command[0], proclimit.Format(memory), proclimit.Variable)
		r.fail(label)
		return
	}
	// Output over the cap replaces every other verdict and prints no diff:
	// a diff of a near-miss of 1 MiB is the dump the cap exists to stop.
	if over := o.overCap(); over != "" {
		fmt.Fprintf(r.out, "FAIL %s: %s\n", label, over)
		r.fail(label)
		return
	}
	if o.timedOut {
		fmt.Fprintf(r.out, "FAIL %s:%s\n", label, o.why(r.cfg.timeout))
		r.fail(label)
		return
	}
	var what []string
	stdoutDiffers := !bytes.Equal(o.stdout, c.out) || !c.hasOut
	stderrDiffers := !bytes.Equal(o.stderr, c.err)
	if stdoutDiffers {
		what = append(what, "stdout differs")
	}
	if stderrDiffers {
		what = append(what, "stderr differs")
	}
	if o.status != c.status {
		what = append(what, fmt.Sprintf("status differs: got %s, want %d", o.statusText(), c.status))
	}
	if budgeted && p.tagged("default") && r.cfg.budget > 0 && o.elapsed > r.cfg.budget {
		what = append(what, fmt.Sprintf("over the default budget %s (ran %s); tag the program slow", r.cfg.budget, seconds(o.elapsed)))
	}
	if len(what) == 0 {
		fmt.Fprintf(r.out, "ok %s (%s)\n", label, seconds(o.elapsed))
		return
	}
	fmt.Fprintf(r.out, "FAIL %s: %s\n", label, strings.Join(what, "; "))
	if stdoutDiffers {
		fmt.Fprintf(r.out, "stdout:\n%s", unifiedDiff(c.out, o.stdout))
	}
	if stderrDiffers {
		fmt.Fprintf(r.out, "stderr:\n%s", unifiedDiff(c.err, o.stderr))
	}
	r.fail(label)
}

// printOutput prints a command's output indented, so no line of it reads as
// a verdict of the runner, or why the command could not start (err).
func (r *runner) printOutput(o outcome, err error) {
	if err != nil {
		fmt.Fprintf(r.out, "    could not start: %v\n", err)
		return
	}
	for _, stream := range [][]byte{o.stdout, o.stderr} {
		for _, line := range lines(stream) {
			fmt.Fprintf(r.out, "    %s\n", line)
		}
	}
}

// removeAll removes a case directory, first making its folders writable, as a
// case may leave a read-only one behind.
func removeAll(dir string) {
	if os.RemoveAll(dir) == nil {
		return
	}
	_ = filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(name, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.2fs", d.Seconds()) }

// outcome is what a command that started did. A command that could not start
// has no outcome: execute returns an error instead, so no caller can read an
// exit status 0 from a command that never ran.
type outcome struct {
	// stdout and stderr hold at most outputCap bytes each; a stream over the
	// cap is marked, and its bytes are dropped.
	stdout, stderr         []byte
	stdoutOver, stderrOver bool
	// status is the exit status, or -1 when a signal ended the process.
	status   int
	signal   string
	timedOut bool
	elapsed  time.Duration
	// limited is a command that failed under a memory limit and whose stderr
	// holds the Go runtime's out-of-memory report.
	limited bool
}

func (o outcome) passed() bool {
	return !o.timedOut && o.status == 0 && !o.stdoutOver && !o.stderrOver
}

func (o outcome) statusText() string {
	if o.status < 0 {
		return "signal " + o.signal
	}
	return strconv.Itoa(o.status)
}

// why says, after a colon, why a command did not finish, or nothing.
func (o outcome) why(timeout time.Duration) string {
	switch {
	case o.timedOut:
		return fmt.Sprintf(" timed out after %s", timeout)
	case o.status < 0:
		return " ended by signal " + o.signal
	}
	return ""
}

// testHookStarted, when not nil, is called by execute on its own goroutine
// right after a command started, its group was registered and its deferred
// stop was armed. It is nil in production; a test sets it to inject a panic
// (net/http's testHook pattern).
var testHookStarted func(argv []string)

// streamDelay bounds how long execute waits, after the last stop of a
// command's group, for its streams to end: a process that left the group
// (setsid) and still holds a stream holds the runner no longer than this, as
// exec.Cmd.WaitDelay bounds it for Go.
const streamDelay = time.Second

// execute runs argv in dir with exactly env, stdin from the file named (or
// empty input), in its own process group, registered with l. Each of its
// streams goes through a pipe into memory, at most outputCap bytes, so no
// output reaches the disk; at the first byte past the cap the stream is over
// and execute stops the group at once. It also stops the group when timeout
// (if not 0) passes, when the leader exits (whatever the leader left running
// goes with it), and when a panic unwinds this goroutine: stopGroup is the
// only signal the runner sends, to the group of a child it started.
// It returns an error, and no outcome, when the command could not start: its
// stdin cannot be opened, its pipes cannot be made, the run is stopping, or
// the command cannot be started.
// A memory limit that is not 0 bounds the command and makes it the first
// process the kernel's out-of-memory killer stops (proclimit.Start).
func (l *live) execute(argv []string, dir string, env []string, stdin string, timeout time.Duration, memory uint64) (outcome, error) {
	input, err := os.Open(os.DevNull)
	if stdin != "" {
		input, err = os.Open(stdin)
	}
	if err != nil {
		return outcome{}, err
	}
	defer input.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return outcome{}, err
	}
	defer outRead.Close()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		outWrite.Close()
		return outcome{}, err
	}
	defer errRead.Close()
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = dir
	command.Env = env
	command.Stdin, command.Stdout, command.Stderr = input, outWrite, errWrite
	ownGroup(command)
	started := time.Now()
	err = l.start(command, memory)
	// The command holds its own copies of the write ends. The runner's go
	// now, or no reader would ever see the end of a stream.
	outWrite.Close()
	errWrite.Close()
	if err != nil {
		return outcome{}, err
	}
	// From here a panic on this goroutine stops the group as it unwinds.
	defer l.release(command.Process)
	stdout, stderr := newCapture(), newCapture()
	overCap := func() { l.stop(command.Process) }
	go stdout.fill(outRead, overCap)
	go stderr.fill(errRead, overCap)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if testHookStarted != nil {
		testHookStarted(argv)
	}
	var o outcome
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-done:
	case <-expired:
		o.timedOut = true
		l.stop(command.Process)
		<-done
	}
	o.elapsed = time.Since(started)
	// The last stop: whatever the leader left running in its group goes with
	// it, and no cap stop signals the group after this.
	l.release(command.Process)
	held := time.NewTimer(streamDelay)
	defer held.Stop()
wait:
	for _, c := range []*capture{stdout, stderr} {
		select {
		case <-c.done:
		case <-held.C:
			break wait
		}
	}
	// A process outside the group that still holds a stream now writes into
	// a closed pipe (EPIPE or SIGPIPE); each fill ends at once.
	outRead.Close()
	errRead.Close()
	<-stdout.done
	<-stderr.done
	o.stdout, o.stdoutOver = stdout.data, stdout.over
	o.stderr, o.stderrOver = stderr.data, stderr.over
	o.status = command.ProcessState.ExitCode()
	if o.status < 0 {
		o.signal = signalName(command.ProcessState)
	}
	o.limited = memory > 0 && o.status > 0 && bytes.Contains(o.stderr, []byte(proclimit.OutOfMemory))
	return o, nil
}
