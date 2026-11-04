// Command examples builds each example program under examples/ with the
// checkout's ergo and compares what it prints with its recorded cases: stdout,
// stderr and the exit status, byte for byte. examples/README.md describes the
// layout, the case format, the tags and the record leg.
//
//	go run ./scripts/examples [flags] [PROGRAM...]
//
// It imports only the standard library: the gate needs only Go and Git.
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

// pendingTest starts what ergo test prints while the compiler has no test
// command (lowering adds it, #196). Revision 3, C4: the skip exists only
// while the checkout's compiler has no ergo test; the merge that joins this
// runner with lower-v1 stage 4 removes it and its tests.
const pendingTest = "ergo test: not available yet"

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
	names   []string
}

type program struct {
	name    string
	dir     string
	tags    []string
	sources []string // the top-level .ergo files, sorted
	port    bool     // holds record.sh and origin
	tested  bool     // declares a test
	cases   []*testCase
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
	scratch, err := os.MkdirTemp("", "ergo-examples-")
	if err != nil {
		fmt.Fprintln(stderr, "examples:", err)
		return 2
	}
	defer os.RemoveAll(scratch)
	r := &runner{cfg: cfg, out: stdout, scratch: scratch}
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
		if testDeclaration.Match(content) {
			p.tested = true
		}
	}
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
	scratch  string
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
	return execute(append([]string{r.cfg.ergo}, args...), r.cfg.root, os.Environ(), "", timeout, r.scratch)
}

// runProgram formats, builds and tests one program and runs its cases.
func (r *runner) runProgram(p *program) {
	if o, err := r.tool(0, append([]string{"fmt", "--check"}, p.sources...)...); err != nil || !o.passed() {
		fmt.Fprintf(r.out, "FAIL %s: fmt\n", p.name)
		r.printOutput(o, err)
		r.fail(p.name + ": fmt")
		return
	}
	dir, err := os.MkdirTemp(r.scratch, "build-")
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s: build: %v\n", p.name, err)
		r.fail(p.name + ": build")
		return
	}
	binary := filepath.Join(dir, p.name)
	if o, err := r.tool(0, "build", filepath.Join(p.dir, "main.ergo"), "-o", binary); err != nil || !o.passed() {
		fmt.Fprintf(r.out, "FAIL %s: build\n", p.name)
		r.printOutput(o, err)
		r.fail(p.name + ": build")
		return
	}
	if p.tested {
		r.testProgram(p)
	}
	for _, c := range p.cases {
		r.runCase(p, c, []string{binary}, true)
	}
}

// testProgram runs the program's own test declarations. A failure fails the
// program; its cases still run.
func (r *runner) testProgram(p *program) {
	o, err := r.tool(r.cfg.timeout, "test", filepath.Join(p.dir, "main.ergo"))
	switch {
	case err != nil:
		fmt.Fprintf(r.out, "FAIL %s: test: could not start: %v\n", p.name, err)
		r.fail(p.name + ": test")
	case o.passed():
		fmt.Fprintf(r.out, "ok %s: test (%s)\n", p.name, seconds(o.elapsed))
	case pendingReply(o):
		fmt.Fprintf(r.out, "skip %s: test (ergo test is not available yet in this compiler)\n", p.name)
	default:
		fmt.Fprintf(r.out, "FAIL %s: test%s\n", p.name, o.why(r.cfg.timeout))
		r.printOutput(o, nil)
		r.fail(p.name + ": test")
	}
}

// pendingReply is the reply of a compiler without ergo test: exit 1, nothing
// on stdout, and stderr that starts with the pending line.
func pendingReply(o outcome) bool {
	return !o.timedOut && o.status == 1 && len(o.stdout) == 0 && bytes.HasPrefix(o.stderr, []byte(pendingTest))
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
		// The goldens come only from a run of the original that started
		// and ended by itself: execCase fails a case whose command could
		// not start, and a timeout or a signal records nothing.
		o, ok := r.execCase(p, c, command)
		if !ok {
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
// So an outcome it returns is always of a command that started.
func (r *runner) execCase(p *program, c *testCase, command []string) (outcome, bool) {
	work, err := os.MkdirTemp("", "ergo-case-")
	if err == nil {
		// HOME and the working directory name the same place, as the
		// program sees it (on darwin TMPDIR goes through a link).
		work, err = filepath.EvalSymlinks(work)
	}
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s/%s: make its directory: %v\n", p.name, c.name, err)
		r.fail(p.name + "/" + c.name)
		return outcome{}, false
	}
	defer removeAll(work)
	env := []string{"HOME=" + work}
	if path, ok := os.LookupEnv("PATH"); ok {
		env = append([]string{"PATH=" + path}, env...)
	}
	env = append(env, c.env...)
	if c.setup {
		o, err := execute([]string{"sh", filepath.Join(c.dir, "setup.sh"), c.dir}, work, env, "", r.cfg.timeout, r.scratch)
		if err != nil || !o.passed() {
			what := fmt.Sprintf("setup.sh exit %d", o.status)
			switch {
			case err != nil:
				what = fmt.Sprintf("setup.sh could not start: %v", err)
			case o.timedOut || o.status < 0:
				what = "setup.sh" + o.why(r.cfg.timeout)
			}
			fmt.Fprintf(r.out, "FAIL %s/%s: %s\n", p.name, c.name, what)
			r.printOutput(o, nil)
			r.fail(p.name + "/" + c.name)
			return outcome{}, false
		}
	}
	o, err := execute(append(slices.Clone(command), c.args...), work, env, c.stdin, r.cfg.timeout, r.scratch)
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s/%s: could not start: %v\n", p.name, c.name, err)
		r.fail(p.name + "/" + c.name)
		return outcome{}, false
	}
	return o, true
}

// runCase runs one case and compares its streams and status with the goldens.
func (r *runner) runCase(p *program, c *testCase, command []string, budgeted bool) {
	o, ok := r.execCase(p, c, command)
	if !ok {
		return
	}
	r.cases++
	label := p.name + "/" + c.name
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
	stdout, stderr []byte
	// status is the exit status, or -1 when a signal ended the process.
	status   int
	signal   string
	timedOut bool
	elapsed  time.Duration
}

func (o outcome) passed() bool { return !o.timedOut && o.status == 0 }

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

// execute runs argv in dir with exactly env, stdin from the file named (or
// empty input), in its own process group. Its output goes to files, so no
// process that keeps a stream open can hold the runner. When the command
// exits, or when timeout (if not 0) passes, execute stops the whole group:
// the only signal the runner sends, to the group of a child it started.
// It returns an error, and no outcome, when the command could not start: its
// stdin cannot be opened, its output files cannot be made, or the command
// cannot be started.
func execute(argv []string, dir string, env []string, stdin string, timeout time.Duration, scratch string) (outcome, error) {
	input, err := os.Open(os.DevNull)
	if stdin != "" {
		input, err = os.Open(stdin)
	}
	if err != nil {
		return outcome{}, err
	}
	defer input.Close()
	stdout, err := os.CreateTemp(scratch, "stdout-")
	if err != nil {
		return outcome{}, err
	}
	defer removeFile(stdout)
	stderr, err := os.CreateTemp(scratch, "stderr-")
	if err != nil {
		return outcome{}, err
	}
	defer removeFile(stderr)
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = dir
	command.Env = env
	command.Stdin, command.Stdout, command.Stderr = input, stdout, stderr
	ownGroup(command)
	started := time.Now()
	if err := command.Start(); err != nil {
		return outcome{}, err
	}
	var o outcome
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
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
		stopGroup(command.Process)
		<-done
	}
	o.elapsed = time.Since(started)
	// Whatever the leader left running in its group goes with it.
	stopGroup(command.Process)
	o.status = command.ProcessState.ExitCode()
	if o.status < 0 {
		o.signal = signalName(command.ProcessState)
	}
	o.stdout, _ = os.ReadFile(stdout.Name())
	o.stderr, _ = os.ReadFile(stderr.Name())
	return o, nil
}

func removeFile(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}
