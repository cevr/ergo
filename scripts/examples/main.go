// Command examples builds each example program under examples/ with the
// checkout's ergo and compares what it prints with its recorded cases: stdout,
// stderr and the exit status, byte for byte. examples/README.md describes the
// layout, the case format, the tags and the record leg.
//
//	go run ./scripts/examples [flags] [PROGRAM...]
//
// It imports the standard library, internal/proclimit for the memory limit
// of the programs it runs, internal/opcount to read a program's workload
// file, and, for the law check (laws.go), the checker's front end: the gate
// needs only Go and Git.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/cases"
	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketchfront"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// knownTags are the words a program's tags file may hold. default stands
// alone; the others keep a program out of the default gate.
var knownTags = []string{"default", "slow", "net", "cc", "tty", "gui", "ts", "mod"}

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
	// lintpack is a Go rule pack (docs/specs/custom-lint.md): its .ergo
	// files are lint fixtures, which examples/lintpack/policy's tests and
	// `ergo lint test` run.
	"lintpack": "a Go rule pack and its lint fixtures",
}

type config struct {
	root    string
	ergo    string
	tags    []string
	timeout time.Duration
	// jobs is how many programs run at once (runAll).
	jobs int
	// testTimeout stops a compiler leg that runs tests (ergo check, ergo
	// test, an explore search and its replay) only when it hangs: those
	// legs build Go and end by themselves, so their time follows the
	// host's load and is no verdict (AGENTS.md, Tests).
	testTimeout time.Duration
	budget      time.Duration
	record      bool
	check       bool
	lawless     bool
	// modules runs only the modules of modules/ (MK0), the names being
	// modules; without it a run with no names runs every module too.
	modules bool
	names   []string
	// cache is the verdict cache's directory (cache.go), or "" for none.
	cache string
	// rewrites is ergo build's -rewrites mode for each program's binary,
	// or "" for the default (EVIDENCE-REPORT: the batch accept runs the
	// examples with -rewrites=proved, whose cases are the default build's
	// goldens, so the outputs of both builds agree).
	rewrites string
	// fullTier runs only the package folders the modules' full-tier
	// files name (modules.go): the gate's full tier runs them, and every
	// other run leaves them out.
	fullTier bool
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
	port    bool     // holds record.sh
	// module marks a program of packages (196b): ergo.mod, the binary's
	// package cmd/NAME/main.ergo and package folders. main is what the
	// build leg builds and the cases run; others are the program's other
	// binaries, cmd/OTHER, which the build leg builds too; tests are the
	// targets of the test leg: every package, sorted.
	module bool
	main   string
	others []string
	tests  []string
	// checked holds, while a program of packages loads, the checked
	// analysis of each of its package folders by absolute path, from one
	// load of them all (checkPackageFolders); checkedDir adds a folder it
	// checks alone. The asset scan and the law rule read it.
	checked map[string]sketchfront.Analysis
	cases   []*testCase
	// explored are the lines of its explored file (explored.go).
	explored []explored
	// library marks a module of modules/ (MK0, modules.go): no binary,
	// no cases; the fmt, gen and test legs run.
	library bool
	// heavy are the package folders of a library's full-tier file, which
	// only a -full-tier run tests; tests holds none of them.
	heavy []string
	// goFiles are a library's .go files by absolute folder, as the layout
	// scan found them; goPackages are the folders moduleGoProblems
	// admitted, the Go packages the module's own externs bind, whose
	// tests the module's go leg runs.
	goFiles    map[string][]string
	goPackages []string
}

func (p *program) tagged(tag string) bool { return slices.Contains(p.tags, tag) }

// testCase is a case folder of a program (internal/cases).
type testCase = cases.Case

// live and outcome are the run's processes and folders and a command's
// run (internal/cases).
type (
	live    = cases.Live
	outcome = cases.Outcome
)

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
	programs, modules, problems := loadLayout(cfg.root, cfg.record)
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintln(stderr, problem)
		}
		fmt.Fprintf(stderr, "examples: %d layout errors; see examples/README.md\n", len(problems))
		return 2
	}
	var selected []*program
	if !cfg.modules && len(cfg.names) > 0 && !slices.ContainsFunc(cfg.names, func(n string) bool { return !strings.HasPrefix(n, modulesDir+"/") }) {
		// Names that are all modules/NAME select those modules, as
		// -modules does: a ledger's killer examples:modules/NAME runs here.
		cfg.modules = true
	}
	if cfg.fullTier {
		cfg.modules = true
	}
	if cfg.modules {
		selected, err = selectModules(cfg.names, modules)
	} else if selected, err = selectPrograms(cfg, programs, modules); err == nil && len(cfg.names) == 0 && !cfg.record {
		// A run of the tags runs every module too: the gate's examples
		// step checks modules/ (MK0).
		selected = append(selected, modules...)
	}
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
	l := cases.NewLive()
	defer l.HandleSignals(stderr, "examples")()
	scratch, err := l.MakeDir("", "ergo-examples-")
	if err != nil {
		fmt.Fprintln(stderr, "examples:", err)
		return 2
	}
	defer l.RemoveDir(scratch)
	r := &runner{cfg: cfg, out: stdout, scratch: scratch, live: l, install: &tsInstall{}, toolEnv: toolEnvironment(os.Environ(), scratch)}
	if !cfg.record {
		r.cache = openCache(cfg.cache, cfg)
	}
	return r.runAll(selected)
}

// programRun is what one program's run left: its output, its counts, and
// the value of a panic of its goroutine, if one ended it.
type programRun struct {
	out      bytes.Buffer
	cases    int
	failures []string
	panicked any
	ended    chan struct{}
}

// runAll runs (or records) the selected programs, at most cfg.jobs at
// once. Each program writes into its own buffer and counts its own cases
// and failures, so its lines stay together and each FAIL line names it.
// The buffers are printed, and the counts summed, in selection order, as
// soon as a program and every program before it have ended: the output is
// the same whatever order the programs end in. A panic of a program's
// goroutine stops the run (as a signal does: no command starts after it,
// and every live group stops), and reaches run's caller once every
// program's goroutine has ended.
func (r *runner) runAll(selected []*program) int {
	runs := make([]*programRun, len(selected))
	for i := range runs {
		runs[i] = &programRun{ended: make(chan struct{})}
	}
	next := make(chan int)
	go func() {
		defer close(next)
		for i := range selected {
			next <- i
		}
	}()
	for range min(r.cfg.jobs, len(selected)) {
		go func() {
			for i := range next {
				r.runOne(selected[i], runs[i])
			}
		}()
	}
	for i, pr := range runs {
		<-pr.ended
		if pr.panicked != nil {
			r.live.Interrupt()
			for _, rest := range runs[i+1:] {
				<-rest.ended
			}
			panic(pr.panicked)
		}
		_, _ = r.out.Write(pr.out.Bytes())
		r.cases += pr.cases
		r.failures = append(r.failures, pr.failures...)
	}
	if r.cache != nil {
		fmt.Fprintf(r.out, "examples: cache %s: %d programs replayed, %d run\n", r.cache.dir, r.cache.hits, r.cache.misses)
	}
	return r.finish()
}

// runOne runs one program with a runner of its own over the run's shared
// state (the configuration, the scratch, the live groups and directories,
// the TypeScript install), and records what it left in pr.
func (r *runner) runOne(p *program, pr *programRun) {
	defer close(pr.ended)
	defer func() { pr.panicked = recover() }()
	one := &runner{cfg: r.cfg, out: &pr.out, scratch: r.scratch, live: r.live, install: r.install, toolEnv: r.toolEnv, cache: r.cache}
	if p.library {
		one.runModule(p)
	} else if r.cfg.record {
		one.recordProgram(p)
	} else {
		one.runCached(p)
	}
	pr.cases, pr.failures = one.cases, one.failures
}

// defaultJobs is how many programs run at once when -jobs is not given:
// GOMAXPROCS when the environment sets it (the gate sets every step's
// GOMAXPROCS to its CPU budget, ERGO_GATE_CPUS), else half the CPUs this
// process may use, at least 1, as the gate's own default budget is: never
// every CPU of the host.
func defaultJobs() int {
	if os.Getenv("GOMAXPROCS") != "" {
		return runtime.GOMAXPROCS(0)
	}
	return max(1, runtime.GOMAXPROCS(0)/2)
}

// hangNet is the default of -timeout and -test-timeout: a case and a test
// leg end by themselves, and their time follows the host's load, so the
// runner stops one only when it hangs and names the hang; no bound fails a
// slow one that still runs (AGENTS.md, Tests: a deadline only stops a hung
// test and names it). A case that must be fast says so with -budget.
const hangNet = 15 * time.Minute

func parseFlags(args []string, stderr io.Writer) (*config, error) {
	flags := flag.NewFlagSet("examples", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cfg := &config{}
	var tags string
	flags.StringVar(&cfg.root, "root", ".", "repository root; programs are the folders `DIR`/examples/*")
	flags.StringVar(&cfg.ergo, "ergo", "bin/ergo", "the compiler; a relative `PATH` is relative to -root")
	flags.StringVar(&tags, "tags", "default", "comma-separated tags; selects the programs whose tags hold any of them")
	flags.DurationVar(&cfg.timeout, "timeout", hangNet, "per case: a safety net that stops a hung case and its process group and names the hang, never a time limit")
	flags.DurationVar(&cfg.testTimeout, "test-timeout", hangNet, "per ergo check, ergo test, explore search and replay: a safety net that stops a hung one, never a time limit")
	flags.DurationVar(&cfg.budget, "budget", 5*time.Second, "CPU time (user and system, never wall time) past which a case of a default program fails; tag the program slow")
	flags.BoolVar(&cfg.record, "record", false, "run each selected port's record.sh in place of the built program and write its goldens")
	flags.BoolVar(&cfg.check, "check", false, "with -record: compare the original's output with the goldens; write nothing")
	flags.BoolVar(&cfg.lawless, "lawless", false, "report every operation type under examples/ with no law, run nothing, and exit 0")
	flags.StringVar(&cfg.cache, "cache", os.Getenv(cacheVariable), "replay a program's recorded pass when every input it reads is unchanged, keyed and kept in `DIR` (default $"+cacheVariable+"; empty: no cache)")
	flags.StringVar(&cfg.rewrites, "rewrites", "", "ergo build's -rewrites `MODE` (on, proved, off) for each program's binary; empty: the default build. The goldens are the default build's, so a pass under another mode is the two builds' agreement")
	flags.BoolVar(&cfg.modules, "modules", false, "run only the modules of modules/: every one, or the MODULE names given")
	flags.BoolVar(&cfg.fullTier, "full-tier", false, "run only the module package folders each module's full-tier file names (the gate's full tier); other runs leave them out")
	flags.IntVar(&cfg.jobs, "jobs", defaultJobs(), "programs run at once; the default is GOMAXPROCS when the environment sets it (the gate's CPU budget), else half the usable CPUs")
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
	if cfg.jobs <= 0 {
		return nil, usageError("-jobs must be positive")
	}
	if cfg.testTimeout <= 0 {
		return nil, usageError("-test-timeout must be positive")
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

// originMoved is the problem of an origin, a file or a folder, in a
// program: a port's provenance lives under conformance/upstream (checkPorts).
const originMoved = "unexpected origin; a port's provenance lives in conformance/upstream/<source>/ports/%s/, never in the example"

// loadExamples reads and checks the layout of every folder of examples/,
// whatever the selection. Each problem names its folder.
// loadLayout loads the programs of examples/ and the modules of modules/
// under root, with every layout error of the programs, the ports'
// provenance (checkPorts) and the modules. run stops on any of them
// before it runs a program; TestRepoLayoutIsClean holds the repository to
// none, in the fast tier, building nothing.
// The root is made absolute first, as parseFlags makes the runner's: the
// package folders' sources are absolute (packageAssetSources), and a
// module's full-tier folders are compared with them.
func loadLayout(root string, record bool) (programs, modules []*program, problems []string) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	programs, problems = loadExamples(root, record)
	modules, moduleProblems := loadModules(root)
	return programs, modules, append(problems, moduleProblems...)
}

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
	return programs, append(problems, checkPorts(root)...)
}

// checkPorts checks the provenance of every port: a folder
// conformance/upstream/<source>/ports/<name>/ (checkOrigin) of a program
// examples/<name>. Provenance lives there, never in the example.
func checkPorts(root string) []string {
	dirs, err := filepath.Glob(filepath.Join(root, "conformance", "upstream", "*", "ports", "*"))
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, dir := range dirs {
		rel, _ := filepath.Rel(root, dir)
		name := filepath.Base(dir)
		if name == "README.md" {
			// The ports' guide (EX-NEUTRAL), not a port.
			continue
		}
		if info, err := os.Stat(filepath.Join(root, "examples", name)); err != nil || !info.IsDir() {
			problems = append(problems, fmt.Sprintf("%s: no program examples/%s; a port's provenance is named after its program", rel, name))
			continue
		}
		for _, problem := range checkOrigin(dir, root) {
			problems = append(problems, rel+": "+problem)
		}
	}
	return problems
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
		case file == "origin":
			delete(present, file)
			problem(originMoved, p.name)
		case file == "main.ergo" || file == "ergo.laws" || file == "README.md" || file == "tags" || file == "record.sh" || file == expectedSkipsFile || file == exploredFile || strings.HasSuffix(file, ".ergo"):
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
				continue
			}
			if strings.HasSuffix(file, ".ergo") {
				p.sources = append(p.sources, filepath.Join(dir, file))
			}
		default:
			problem("unexpected %s; a program folder holds main.ergo, README.md, tags and cases/, and may hold record.sh, expected-skips, explored, testdata/, ergo.laws and other .ergo files", file)
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
// being the program folder's name) with its main.ergo, any other binary
// as cmd/OTHER with its main.ergo, and package
// folders of .ergo files, beside the files every program holds. Top-level
// .ergo files are the module root's package.
func loadModuleProgram(p *program, entries []os.DirEntry, record bool) (*program, []string) {
	p.checked = loadPackageFolders(p.dir, entries)
	defer func() { p.checked = nil }()
	var problems []string
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	present := map[string]bool{}
	for _, entry := range entries {
		file := entry.Name()
		present[file] = true
		switch {
		case file == "cases" || file == "testdata" || file == "ergo.bind":
			// ergo.bind holds the generated bindings of the module's Go
			// packages (IG7): not package source.
			if !entry.IsDir() {
				problem("%s is not a folder", file)
			}
		case file == "origin":
			delete(present, file)
			problem(originMoved, p.name)
		case file == "go.mod" || file == "go.sum":
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
			}
		case moduleRootFile(file) || file == "tags" || file == "record.sh" || file == exploredFile:
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
				continue
			}
			if strings.HasSuffix(file, ".ergo") {
				p.sources = append(p.sources, filepath.Join(p.dir, file))
			}
		case file == opcount.WorkloadFile && !entry.IsDir():
			// scripts/opcount's sizes and arguments (examples/README.md,
			// "Workload"); the runner checks it and does not run it. A
			// folder of that name is a package (examples/vt's workload).
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
				continue
			}
			_, found, _ := opcount.Check(filepath.Dir(filepath.Dir(p.dir)), p.dir)
			for _, w := range found {
				problem("%s: %s", file, w)
			}
		case file == opcount.WorkloadFile:
			// A workload package (its program's gen documents) holds the
			// workload file, so scripts/opcount counts every program that
			// has a workload (examples/README.md, "Workload").
			at := opcount.WorkloadFile + "/" + opcount.WorkloadFile
			_, found, ok := opcount.Check(filepath.Dir(filepath.Dir(p.dir)), p.dir)
			if !ok {
				problem("%s: a workload package holds its workload file, for scripts/opcount", at)
			}
			for _, w := range found {
				problem("%s: %s", at, w)
			}
			spec, err := filepath.Abs(opcount.Path(p.dir))
			if err != nil {
				problem("%s: %v", at, err)
			}
			problems = append(problems, packageAssetSources(p, filepath.Join(p.dir, file), file, map[string]bool{spec: true})...)
		case file == "ts" && entry.IsDir():
			problems = append(problems, tsSources(filepath.Join(p.dir, file))...)
		case entry.IsDir():
			if file == "cmd" {
				problems = append(problems, checkCommands(p, filepath.Join(p.dir, file))...)
			}
			problems = append(problems, packageSources(p, filepath.Join(p.dir, file), file)...)
		default:
			problem("unexpected %s; a program of packages holds ergo.mod, cmd/%s/main.ergo, package folders, README.md, tags and cases/, and may hold record.sh, expected-skips, explored, workload, testdata/, ergo.laws, and go.mod, go.sum and ergo.bind/ for its Go packages", file, p.name)
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

// checkCommands checks that cmd holds a folder per binary, each with its
// main.ergo (AGENTS.md: each binary's cmd/<name>/main.ergo): the
// program's own, cmd/NAME, whose binary the cases run, and any others,
// which the build leg builds (p.others, sorted).
func checkCommands(p *program, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	own := false
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			problems = append(problems, fmt.Sprintf("cmd/%s is not a folder; cmd holds a folder per binary, cmd/%s first", name, p.name))
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, name, "main.ergo")); err != nil || !info.Mode().IsRegular() {
			problems = append(problems, fmt.Sprintf("no cmd/%s/main.ergo", name))
			continue
		}
		if name == p.name {
			own = true
		} else {
			p.others = append(p.others, filepath.Join(dir, name))
		}
	}
	if !own && len(problems) == 0 {
		problems = append(problems, fmt.Sprintf("cmd holds the program's binary, cmd/%s", p.name))
	}
	slices.Sort(p.others)
	return problems
}

// checkPackageFolders checks the package folders of a program of packages
// (the folders below dir, cases, testdata, ergo.bind, origin and ts left
// out, that hold an .ergo file, and dir itself when it does) with one load
// (sketchfront.CheckPackages): the std packages and the module's packages
// they import are read and checked once, not once per folder and again
// for the law rule. Two in-process checks of every folder were 17 CPU-s
// of the examples step's 120 after a checker edit, on every run, replays
// included (gate-speed GS31a). A load that cannot be shared (a
// diagnostic, a binding) gives no analysis: each folder is then checked
// alone (checkedDir), as before.
func checkPackageFolders(dir string, entries []os.DirEntry) map[string]sketchfront.Analysis {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	var dirs []string
	holdsErgo := func(folder string) bool {
		files, err := os.ReadDir(folder)
		if err != nil {
			return false
		}
		return slices.ContainsFunc(files, func(f os.DirEntry) bool {
			return strings.HasSuffix(f.Name(), ".ergo") && f.Type().IsRegular()
		})
	}
	if holdsErgo(dir) {
		dirs = append(dirs, dir)
	}
	for _, entry := range entries {
		switch name := entry.Name(); {
		case !entry.IsDir() || name == "cases" || name == "testdata" || name == "ergo.bind" || name == "origin" || name == "ts":
			continue
		}
		_ = filepath.WalkDir(filepath.Join(dir, entry.Name()), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && holdsErgo(path) {
				dirs = append(dirs, path)
			}
			return nil
		})
	}
	if len(dirs) == 0 {
		return nil
	}
	analyses, err := sketchfront.CheckPackages(dirs, gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		return nil
	}
	checked := make(map[string]sketchfront.Analysis, len(dirs))
	for i, folder := range dirs {
		checked[folder] = analyses[i]
	}
	return checked
}

// loadPackageFolders is checkPackageFolders; a test sets it to load
// nothing, the reference of every folder checked alone.
var loadPackageFolders = checkPackageFolders

// checkedDir is the checked analysis of the package folder dir, an
// absolute path: the program's shared load's (checkPackageFolders), or the
// folder checked alone and kept, so no folder is checked twice.
func (p *program) checkedDir(dir string) (sketchfront.Analysis, error) {
	if a, ok := p.checked[dir]; ok {
		return a, nil
	}
	a, err := sketchfront.CheckDir(dir)
	if err == nil && p.checked != nil {
		p.checked[dir] = a
	}
	return a, err
}

// packageSources adds the .ergo files of the package folder dir, and of
// the package folders under it, to the program's sources. A package
// folder holds .ergo files, its generation marker if any, and package folders.
func packageSources(p *program, dir, label string) []string {
	return packageAssetSources(p, dir, label, map[string]bool{})
}

// packageAssetSources admits only assets the compiler actually selected.
// A stray text file remains a layout error; asset directories are not
// mistaken for Ergo packages that the test leg must run.
func packageAssetSources(p *program, dir, label string, assets map[string]bool) []string {
	dir, absErr := filepath.Abs(dir)
	if absErr != nil {
		return []string{absErr.Error()}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".ergo") {
			if a, err := p.checkedDir(dir); err == nil {
				for _, name := range a.EmbeddedPaths() {
					assets[name] = true
				}
			}
			break
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			problems = append(problems, packageAssetSources(p, filepath.Join(dir, name), label+"/"+name, assets)...)
		case strings.HasSuffix(name, ".ergo") && entry.Type().IsRegular():
			p.sources = append(p.sources, filepath.Join(dir, name))
		case name == genmark.Name && entry.Type().IsRegular():
			// genProgram reads the marker, including for stale output.
		case assets[filepath.Join(dir, name)] && entry.Type().IsRegular():
		case p.library && strings.HasSuffix(name, ".go") && entry.Type().IsRegular():
			// A module's own Go (docs/spec/interop.md, Trusted Go 3):
			// moduleGoProblems admits the folder only when an extern go
			// of the module binds its package.
			if p.goFiles == nil {
				p.goFiles = map[string][]string{}
			}
			p.goFiles[dir] = append(p.goFiles[dir], name)
		case p.library && strings.HasSuffix(name, ".s"):
			problems = append(problems, fmt.Sprintf("%s/%s is assembly; a module's own Go body is written in Go (docs/spec/interop.md, Trusted Go 3)", label, name))
		default:
			problems = append(problems, fmt.Sprintf("unexpected %s/%s; a package folder holds .ergo files, an optional %s, and package folders", label, name, genmark.Name))
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
	p.port = present["record.sh"]
	if present["tags"] {
		tags, err := readTags(filepath.Join(dir, "tags"))
		if err != nil {
			problem("tags: %v", err)
		}
		p.tags = tags
	}
	if present["ts"] != p.tagged("ts") {
		problem("a ts/ folder and the tag ts go together: the ts leg runs the TypeScript side in ts/")
	}
	if p.tagged("mod") && !(p.module && present["go.mod"]) {
		problem("tagged mod: the mod leg checks the modules of the go.mod beside ergo.mod, and this program has none")
	}
	for _, source := range p.sources {
		// The test leg tests every package, as go test ./... does: the
		// compiler decides what a package's test build runs, its tests,
		// its laws and the laws of other packages that its providers
		// bind (law-xpkg), so a package that declares no test or law may
		// still run one (fu6b, G-ma-1). One rule for a program of one
		// folder and a program of packages.
		target := p.main
		if p.module {
			target = filepath.Dir(source)
		}
		if !slices.Contains(p.tests, target) {
			p.tests = append(p.tests, target)
		}
	}
	if p.module && present["go.mod"] {
		for _, dir := range localModules(p) {
			for _, target := range testsOfModule(dir) {
				if !slices.Contains(p.tests, target) {
					p.tests = append(p.tests, target)
				}
			}
		}
	}
	slices.Sort(p.tests)
	if present[exploredFile] {
		lines, found := readExplored(p)
		problems = append(problems, found...)
		p.explored = lines
	}
	if present["cases"] {
		loaded, found := cases.Load(filepath.Join(dir, "cases"), record)
		problems = append(problems, found...)
		p.cases = loaded
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

func selectPrograms(cfg *config, programs, modules []*program) ([]*program, error) {
	var selected []*program
	if len(cfg.names) > 0 {
		// A name may also be a module, modules/NAME: the mutant runner's
		// examples suite names one so (examples:modules/NAME).
		programs := slices.Concat(programs, modules)
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
	cfg     *config
	out     io.Writer
	scratch string // holds only the built programs
	live    *live
	// toolEnv is the environment of every compiler command
	// (toolEnvironment).
	toolEnv  []string
	cases    int
	failures []string
	// ts is the TypeScript run of the program whose cases run now, or
	// nil (ts.go); install is the run's one install, which every
	// program's runner shares.
	ts      *tsRun
	install *tsInstall
	// cache is the run's verdict cache, or nil (cache.go).
	cache *verdictCache
	// keyed holds the compiler calls the program's key made (legKey), by
	// their arguments: a leg that makes the same call takes its result
	// instead of running it again (gate-speed GS20).
	keyed map[string]outcome
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
// and empty input, in its own process group. A check runs with the
// law-verdict store off (lawCacheOff): the key's check, a check leg and a
// program of packages' check alike, so a leg takes the key's result
// (keyed) and no check stores a verdict the native test leg would print
// cached.
func (r *runner) tool(timeout time.Duration, args ...string) (outcome, error) {
	return r.toolWith(nil, timeout, args...)
}

// toolWith is tool with the variables extra added to its environment.
func (r *runner) toolWith(extra []string, timeout time.Duration, args ...string) (outcome, error) {
	if o, ok := r.keyed[keyedCall(args)]; ok {
		return o, nil
	}
	env, report := append(slices.Clip(r.toolEnv), extra...), ""
	// test --emitted --json checks each package too (GS31b).
	if len(args) > 0 && (args[0] == "check" || args[0] == "test" && slices.Contains(args, "--json")) {
		env = append(slices.Clip(env), lawCacheOff)
	}
	for _, kv := range extra {
		// A caller that reads the stage report itself (GS32a′) names its
		// file; the IR check reads the same file.
		if v, ok := strings.CutPrefix(kv, stageReportVariable+"="); ok {
			report = v
		}
	}
	if len(args) > 0 && args[0] == "test" && report == "" {
		if f, err := os.CreateTemp(r.scratch, "stage-*.json"); err == nil {
			report = f.Name()
			f.Close()
			defer os.Remove(report)
			env = append(slices.Clone(env), stageReportVariable+"="+report)
		}
	}
	o, err := r.live.Execute(append([]string{r.cfg.ergo}, args...), r.cfg.root, env, "", timeout, 0)
	if report != "" {
		r.testsThroughIR(args, report)
	}
	return o, err
}

// testsThroughIR fails the run when an `ergo test` call placed a test
// binary lowered as Go source text (the stage report's binaries_text):
// every test binary lowers through IR, as `ergo build` ships it, so the
// tests check the code a build ships (TEQb). A report the command did not
// write (it was stopped) says nothing.
func (r *runner) testsThroughIR(args []string, report string) {
	data, err := os.ReadFile(report)
	if err != nil || len(data) == 0 {
		return
	}
	var counts map[string]int64
	if json.Unmarshal(data, &counts) != nil {
		return
	}
	if n := counts["binaries_text"]; n > 0 {
		call := "ergo " + strings.Join(args, " ")
		fmt.Fprintf(r.out, "FAIL %s: %d test binaries lowered as Go source text, not through IR\n", call, n)
		r.fail(call + ": text lowering")
	}
}

// lawCacheOff turns the law-verdict store off for a check (lawcache): every
// law it reports it evaluated, and it stores none. A verdict a check
// stored would be printed "(cached)" by the native test leg of the same
// run, and the check/native comparison would be of one evaluation with
// itself (checkTarget). Before, the key's check ran with the store on and
// a check leg with it off: a missed program checked each target twice
// (gate-speed GS30).
const lawCacheOff = "ERGO_LAWCACHE=off"

// emissionCacheVariable is build.RootVariable, the emission cache root.
const emissionCacheVariable = "ERGO_CACHE"

// toolEnvironment is environ with the emission cache root set to a folder
// of the run's scratch, whatever the caller set: the runner is a check, and
// its programs would fill the user's cache (docs/tooling.md, Emission cache
// bound). The folder goes with the scratch when the run ends.
//
// It drops the caller's ERGO_BUILD and ERGO_STAGE_REPORT. ERGO_BUILD=full
// turns ergo's warm answer off, and `ergo test DIR/...` then links one
// shared test module (TEST-one) where the key hashed each package's own
// module (legKey); the runner names its own stage report (testModule)
// (gate-speed GS32a′).
func toolEnvironment(environ []string, scratch string) []string {
	env := slices.DeleteFunc(slices.Clone(environ), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return name == emissionCacheVariable || name == warmVariable || name == stageReportVariable
	})
	return append(env, emissionCacheVariable+"="+filepath.Join(scratch, "cache"))
}

// warmVariable is sketchfront.WarmVariable, and stageReportVariable
// sketchfront.StageReportVariable.
const (
	warmVariable        = "ERGO_BUILD"
	stageReportVariable = "ERGO_STAGE_REPORT"
)

// sharedTestModules is the stage report's count of the shared test modules
// `ergo test DIR/...` linked (stage.SharedTestModules).
const sharedTestModules = "shared_test_modules"

// toolFailed prints the failure of a compiler leg (fmt, fix or build) and counts
// it. Output over the cap is named, never printed.
func (r *runner) toolFailed(p *program, leg string, o outcome, err error) {
	if over := o.OverCap(); err == nil && over != "" {
		fmt.Fprintf(r.out, "FAIL %s: %s: %s\n", p.name, leg, over)
	} else {
		fmt.Fprintf(r.out, "FAIL %s: %s\n", p.name, leg)
		r.printOutput(o, err)
	}
	r.fail(p.name + ": " + leg)
}

// runProgram formats, fixes, builds and tests one program and runs its
// cases. The fix leg is `ergo fix --check` of each package (q137: every
// function writes its clauses): a clause ergo fix would write fails the
// program, as an unformatted file does.
func (r *runner) runProgram(p *program) {
	if p.tagged("mod") && !r.modLeg(p) {
		return
	}
	if o, err := r.tool(0, append([]string{"fmt", "--check"}, p.sources...)...); err != nil || !o.Passed() {
		r.toolFailed(p, "fmt", o, err)
		return
	}
	for _, target := range p.tests {
		if o, err := r.tool(0, "fix", "--check", target); err != nil || !o.Passed() {
			r.toolFailed(p, "fix", o, err)
			return
		}
	}
	if !r.genProgram(p) {
		return
	}
	dir, err := os.MkdirTemp(r.scratch, "build-")
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s: build: %v\n", p.name, err)
		r.fail(p.name + ": build")
		return
	}
	binary := filepath.Join(dir, p.name)
	if o, err := r.tool(0, r.buildArgs(p.main, "-o", binary)...); err != nil || !o.Passed() {
		r.toolFailed(p, "build", o, err)
		return
	}
	for _, other := range p.others {
		leg := "build cmd/" + filepath.Base(other)
		if o, err := r.tool(0, "build", other, "-o", filepath.Join(dir, filepath.Base(other))); err != nil || !o.Passed() {
			r.toolFailed(p, leg, o, err)
			return
		}
	}
	r.testProgram(p)
	r.exploreProgram(p)
	command := []string{binary}
	if p.tagged("ts") {
		deps, err := r.tsDeps()
		if err != nil {
			fmt.Fprintf(r.out, "FAIL %s: ts: %v\n", p.name, err)
			r.fail(p.name + ": ts")
			return
		}
		r.ts = &tsRun{deps: deps, binary: binary}
		defer func() { r.ts = nil }()
		command = []string{"bun"}
	}
	for _, c := range p.cases {
		r.runCase(p, c, command, true)
	}
}

// testProgram runs the test build of each package of the program: `ergo
// test` of its main.ergo, or for a program of packages one `ergo test
// DIR/...`, which checks the module once and links one test binary for
// every package (TEST-one), its report split at each `package P` line. A
// run that does not pass is run again one package at a time, so each
// failure is reported at its package as before. A failure fails the
// program; its cases still run.
//
// A program's checked-in law ledger (ergo.laws, runtime.md Tests 10) must
// be the one its passing ergo test writes: a stale one keeps the reference
// lowering at the sites it names. ergo test rewrites the file in place, so
// the runner keeps its bytes, fails the ledger leg when the run changed
// them, and puts them back: a run of the runner leaves the tree as it was.
func (r *runner) testProgram(p *program) {
	ledger := filepath.Join(p.dir, "ergo.laws")
	kept, err := os.ReadFile(ledger)
	tracked := err == nil
	if !(p.module && len(p.tests) > 1 && r.testModule(p)) {
		for _, target := range p.tests {
			leg := "test"
			if p.module {
				rel, _ := filepath.Rel(p.dir, target)
				leg = "test " + filepath.ToSlash(rel)
			}
			r.testTarget(p, target, leg)
		}
	}
	if !tracked {
		return
	}
	if now, err := os.ReadFile(ledger); err == nil && bytes.Equal(now, kept) {
		return
	}
	if err := os.WriteFile(ledger, kept, 0o644); err != nil {
		fmt.Fprintf(r.out, "FAIL %s: ledger: ergo test changed ergo.laws, and the runner could not put it back: %v\n", p.name, err)
	} else {
		fmt.Fprintf(r.out, "FAIL %s: ledger: ergo test changed the checked-in ergo.laws (put back); run ergo test in %s and commit the file it writes\n", p.name, p.dir)
	}
	r.fail(p.name + ": ledger")
}

// testModule runs every package of the program of packages p in one `ergo
// test DIR/...` and reports each package's leg, as testTarget does, from
// its part of the report: the check leg's evaluation against it and its ok
// line (an unexpected skip fails ergo test itself, runtime.md Tests 4). It reports false, having printed
// nothing, when the run did not pass; testProgram then tests each package
// alone. A run that passed but did not test exactly the program's packages
// fails the program.
func (r *runner) testModule(p *program) bool {
	module, err := moduleName(filepath.Join(p.dir, "ergo.mod"))
	if err != nil {
		return false
	}
	pattern := filepath.Join(p.dir, "...")
	report := filepath.Join(r.scratch, "stage-"+p.name+".json")
	o, err := r.toolWith([]string{stageReportVariable + "=" + report}, r.cfg.timeout*time.Duration(len(p.tests)), "test", pattern)
	if err != nil || o.OverCap() != "" || o.TimedOut || !o.Passed() {
		return false
	}
	// The key hashed each package's own test module (legKey), so the run
	// must have linked those: one that linked the shared module (TEST-one)
	// ran Go the key does not hold, and the verdict is not kept (GS32a′).
	if _, keyed := r.keyed[keyedCall([]string{"test", "--emitted", "--json", pattern})]; keyed {
		if why := linkedPerPackage(report); why != "" {
			fmt.Fprintf(r.out, "FAIL %s: test: %s; its key holds each package's own test module\n", p.name, why)
			r.fail(p.name + ": test")
			return true
		}
	}
	sections, order := splitPackages(o.Stdout)
	var want, inside []string
	var outside []string // a local module's packages (localModules): DIR/... does not run them
	for _, target := range p.tests {
		rel, _ := filepath.Rel(p.dir, target)
		if rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
			outside = append(outside, target)
			continue
		}
		inside = append(inside, target)
		want = append(want, path.Join(module, filepath.ToSlash(rel)))
	}
	defer func() {
		for _, target := range outside {
			rel, _ := filepath.Rel(p.dir, target)
			r.testTarget(p, target, "test "+filepath.ToSlash(rel))
		}
	}()
	if got := slices.Sorted(slices.Values(order)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		fmt.Fprintf(r.out, "FAIL %s: test: ergo test ran the packages %s; the program's packages are %s\n", p.name, strings.Join(got, " "), strings.Join(want, " "))
		r.fail(p.name + ": test")
		return true
	}
	// The check legs: one `ergo check DIR/...` too, or each package alone
	// when that run is not one valid envelope per package.
	evaluations := r.checkModule(p, order)
	for i, target := range inside {
		rel, _ := filepath.Rel(p.dir, target)
		leg := "test " + filepath.ToSlash(rel)
		section := sections[want[i]]
		evaluation := evaluations[want[i]]
		if evaluations == nil {
			evaluation = r.checkTarget(p, target, strings.Replace(leg, "test", "check", 1))
		}
		if evaluation != nil {
			r.compareEvaluation(p, strings.Replace(leg, "test", "check", 1), evaluation, section)
		}
		fmt.Fprintf(r.out, "ok %s: %s (in one run of %s)\n", p.name, leg, seconds(o.Elapsed))
	}
	return true
}

// linkedPerPackage says why the stage report at path does not show a run
// that linked each package's own test module, or "": it names no shared
// test module. A report that cannot be read fails closed.
func linkedPerPackage(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "ergo test wrote no stage report"
	}
	var counts map[string]int64
	if err := json.Unmarshal(data, &counts); err != nil {
		return "ergo test's stage report is not JSON"
	}
	n, ok := counts[sharedTestModules]
	if !ok {
		return "ergo test's stage report has no " + sharedTestModules
	}
	if n > 0 {
		return fmt.Sprintf("ergo test linked %d shared test module", n)
	}
	return ""
}

// splitPackages splits the stdout of `ergo test DIR/...` at each `package
// P` line: the lines after it, by P, and the packages in report order.
func splitPackages(stdout []byte) (map[string][]byte, []string) {
	sections := map[string][]byte{}
	var order []string
	current := ""
	for _, line := range strings.SplitAfter(string(stdout), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "package "); ok {
			current = name
			order = append(order, name)
			sections[name] = []byte{}
			continue
		}
		if current != "" {
			sections[current] = append(sections[current], line...)
		}
	}
	return sections, order
}

func (r *runner) testTarget(p *program, target, leg string) {
	evaluation := r.checkTarget(p, target, strings.Replace(leg, "test", "check", 1))
	// The run gets a TMPDIR of its own, which must be empty after it: a
	// law case or a test under TestAmbient makes its folder there,
	// and the runner removes each when its case ends (rows.md, Test's ambient).
	tmp, err := os.MkdirTemp(r.scratch, "test-tmp-")
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s: %s: %v\n", p.name, leg, err)
		r.fail(p.name + ": " + leg)
		return
	}
	defer os.RemoveAll(tmp)
	o, err := r.live.Execute([]string{r.cfg.ergo, "test", target}, r.cfg.root, append(slices.Clip(r.toolEnv), "TMPDIR="+tmp), "", r.cfg.testTimeout, 0)
	// Only a run that ended by itself is held to it: a hang or the output
	// cap stops the group, and the stopped ergo removes nothing, so that
	// run fails for its own reason below.
	if left, _ := os.ReadDir(tmp); err == nil && o.Passed() && len(left) > 0 {
		fmt.Fprintf(r.out, "FAIL %s: %s: ergo test left %s in its TMPDIR; each case's folder is removed when the case ends (rows.md, Test's ambient)\n", p.name, leg, left[0].Name())
		r.fail(p.name + ": " + leg)
		return
	}
	if evaluation != nil && err == nil && o.OverCap() == "" && !o.TimedOut {
		r.compareEvaluation(p, strings.Replace(leg, "test", "check", 1), evaluation, o.Stdout)
	}
	switch {
	case err != nil:
		fmt.Fprintf(r.out, "FAIL %s: %s: could not start: %v\n", p.name, leg, err)
		r.fail(p.name + ": " + leg)
	case o.OverCap() != "":
		fmt.Fprintf(r.out, "FAIL %s: %s: %s\n", p.name, leg, o.OverCap())
		r.fail(p.name + ": " + leg)
	case o.Passed():
		fmt.Fprintf(r.out, "ok %s: %s (%s)\n", p.name, leg, seconds(o.Elapsed))
	default:
		fmt.Fprintf(r.out, "FAIL %s: %s%s\n", p.name, leg, o.Why(r.cfg.testTimeout))
		r.printOutput(o, nil)
		r.fail(p.name + ": " + leg)
	}
}

// expectedSkipsFile is the file beside a module's ergo.mod that lists the
// skip lines its ergo test may print; ergo test fails on any other
// (runtime.md, Tests 4). The runner admits it in a program's layout.
const expectedSkipsFile = "expected-skips"

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
		if over := o.OverCap(); over != "" {
			fmt.Fprintf(r.out, "FAIL %s/%s: %s\n", p.name, c.Name, over)
			r.fail(p.name + "/" + c.Name)
			continue
		}
		if o.TimedOut || o.Status < 0 {
			fmt.Fprintf(r.out, "FAIL %s/%s:%s\n", p.name, c.Name, o.Why(r.cfg.timeout))
			r.fail(p.name + "/" + c.Name)
			continue
		}
		if err := writeGoldens(c.Dir, o); err != nil {
			fmt.Fprintf(r.out, "FAIL %s/%s: write goldens: %v\n", p.name, c.Name, err)
			r.fail(p.name + "/" + c.Name)
			continue
		}
		r.cases++
		fmt.Fprintf(r.out, "recorded %s/%s\n", p.name, c.Name)
	}
}

// writeGoldens writes expected.out always, expected.err only when stderr is
// not empty and expected.status only when the status is not 0, removing a
// stale one otherwise.
func writeGoldens(dir string, o outcome) error {
	if err := os.WriteFile(filepath.Join(dir, "expected.out"), o.Stdout, 0o644); err != nil {
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
	if len(o.Stderr) > 0 {
		stderr = o.Stderr
	}
	if o.Status != 0 {
		status = []byte(strconv.Itoa(o.Status) + "\n")
	}
	if err := optional("expected.err", stderr); err != nil {
		return err
	}
	return optional("expected.status", status)
}

// execCase runs one case's setup.sh and command in a fresh empty directory
// (cases.Live.Exec). It prints a FAIL line and returns false when the case
// could not run: its directory, its setup.sh or its command could not
// start, or setup.sh failed. So an outcome it returns is always of a
// command that started. The command runs under the memory limit memory (0:
// none).
func (r *runner) execCase(p *program, c *testCase, command []string, memory uint64) (outcome, bool) {
	opt := cases.Options{Timeout: r.cfg.timeout, Memory: memory}
	if r.ts != nil {
		opt.Prepare = func(_ string, env []string) (string, []string, func(), error) {
			copied, err := r.tsCopy(p, r.ts.deps)
			if err != nil {
				return "", nil, nil, fmt.Errorf("copy the program for its TypeScript: %w", err)
			}
			return copied, r.ts.env(r.cfg.ergo, env), func() { r.live.RemoveDir(copied) }, nil
		}
	}
	o, f := r.live.Exec(c, command, opt)
	if f != nil {
		fmt.Fprintf(r.out, "FAIL %s/%s: %s\n", p.name, c.Name, f.What)
		if f.Output != nil {
			r.printOutput(*f.Output, nil)
		}
		r.fail(p.name + "/" + c.Name)
		return outcome{}, false
	}
	return o, true
}

// runCase runs one case and compares its streams and status with the goldens
// (cases.Judge). budgeted marks the built program, which also runs under the
// memory limit; a port's original (record.sh) runs under neither.
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
	label := p.name + "/" + c.Name
	v := cases.Judge(c, o, command[0], memory, r.cfg.timeout)
	if v.Fatal != "" {
		fmt.Fprintf(r.out, "FAIL %s: %s\n", label, v.Fatal)
		r.fail(label)
		return
	}
	what := v.What
	// The budget charges CPU time, never wall time: a healthy case under
	// the host's load takes longer but does no more work (AGENTS.md, Tests:
	// no wall-clock bounds; owner, 2026-11-12).
	if budgeted && p.tagged("default") && r.cfg.budget > 0 && o.CPU > r.cfg.budget {
		what = append(what, fmt.Sprintf("over the default budget %s of CPU time (used %s); tag the program slow", r.cfg.budget, seconds(o.CPU)))
	}
	if len(what) == 0 {
		fmt.Fprintf(r.out, "ok %s (%s)\n", label, seconds(o.Elapsed))
		return
	}
	fmt.Fprintf(r.out, "FAIL %s: %s\n", label, strings.Join(what, "; "))
	v.WriteDiff(r.out, c, o)
	r.fail(label)
}

// printOutput prints a command's output indented, so no line of it reads as
// a verdict of the runner, or why the command could not start (err).
func (r *runner) printOutput(o outcome, err error) {
	if err != nil {
		fmt.Fprintf(r.out, "    could not start: %v\n", err)
		return
	}
	for _, stream := range [][]byte{o.Stdout, o.Stderr} {
		for _, line := range cases.Lines(stream) {
			fmt.Fprintf(r.out, "    %s\n", line)
		}
	}
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.2fs", d.Seconds()) }
