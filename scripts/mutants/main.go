// Command mutants runs the mutant ledgers of the checker and the lowering:
// each mutant is an edit of the package's source that some test, law or
// example must notice. docs/tooling.md ("Mutant ledger") describes the
// ledger, the runner and when to run it.
//
//	go run ./scripts/mutants [flags] PACKAGE...
//
// PACKAGE is check, lower, a package directory, or a ledger file. The
// runner works on copies of the checkout under -work and never writes the
// checkout, except the ledgers with -write. It first measures per-test
// coverage (once per tree: the result is kept under -work, by default a
// private directory removed at the end): every
// top-level test of the check, lower and std packages and every default
// example program, each in its own process, with coverage of check and
// lower (the ergo CLI and the examples runner built with coverage, their
// main packages included); a suite whose segment the coverage store holds
// at its unchanged key is not measured again (coverstore.go). Then, for
// each mutant, it applies the edit,
// builds what the covering tests need, and runs only the tests that cover
// the mutated lines, the ledger's known killers first and then the
// cheapest, until the first one fails. Each child runs as the leader of its
// own process group, under a memory limit (RLIMIT_DATA, internal/proclimit)
// and a deadline derived from the coverage pass, and its group is stopped
// when it ends. At the end each copy is checked against the checkout: a
// mutant left in place is an error.
//
// It exits 1 when a mutant survives that the ledger does not record as a
// gap, 2 when the run itself fails.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/scripts/mutants/suitename"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// privateWork is the default -work: a directory of this run alone, which
// done removes. A fixed shared default let two runs at once overwrite each
// other's coverage counters (batch-18 verify, On base: "open
// /tmp/ergo-mutants/cover/counters/37: no such file"). Pass -work to keep
// the coverage for a later run.
func privateWork() (dir string, done func(), err error) {
	dir, err = os.MkdirTemp("", "ergo-mutants-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mutants", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cfg := Config{Log: stderr}
	var selectRE, memory, jsonOut, ergoFlag string
	var write, fresh, measureOnly, compileOnly, resume, noNativeErgo bool
	flags.BoolVar(&compileOnly, "compiles", false, "apply each selected row and compile it, and run no test: list the rows that do not compile (invalid)")
	flags.StringVar(&cfg.Root, "root", ".", "the checkout")
	flags.StringVar(&ergoFlag, "ergo", "", "path to the ergo binary for delegating .ergo rows to ergo test --mutants (default: <root>/bin/ergo or ergo on PATH)")
	flags.BoolVar(&noNativeErgo, "no-native-ergo", false, "disable delegation of .ergo rows to ergo test --mutants and judge every row on the Go worker path")
	flags.BoolVar(&fresh, "fresh", false, "judge every selected row, also one whose killed verdict is stored at its unchanged key, and measure every suite's coverage (the reference)")
	flags.BoolVar(&measureOnly, "measure", false, "measure the coverage of the ledgers' suites into the coverage store and judge nothing (run on main's head after a landing)")
	flags.StringVar(&cfg.Work, "work", "", "scratch `DIR`: the copies, the binaries and the kept coverage (default: a private directory, removed at the end)")
	flags.StringVar(&selectRE, "run", "", "judge only the mutants whose name matches `REGEXP`")
	flags.BoolVar(&cfg.All, "all", false, "run every covering test of each mutant, to name every killer")
	flags.BoolVar(&write, "write", false, "record the killers found (and the gaps of mutants judged for the first time) in the ledgers")
	flags.IntVar(&cfg.Workers, "workers", 2, "mutants judged at once, each in its own copy")
	flags.IntVar(&cfg.Tests, "tests", 2, "tests of one mutant run at once, until the first kill (workers × tests children run at once, each under -memory)")
	flags.BoolVar(&resume, "resume", true, "keep a journal of the tree's verdicts under the user cache directory, and take the verdicts an earlier, unfinished run of the same tree wrote there")
	flags.IntVar(&cfg.Procs, "procs", 2, "GOMAXPROCS and go -p of each child")
	flags.StringVar(&memory, "memory", "4GiB", "RLIMIT_DATA of each child (KiB, MiB, GiB, or 0 for none)")
	flags.StringVar(&cfg.Coverage, "coverage", "", "read the coverage from `FILE` instead of measuring it")
	flags.DurationVar(&cfg.CoverDeadline, "cover-deadline", time.Hour, "deadline of each child of the coverage pass")
	flags.StringVar(&jsonOut, "json", "", "write one JSON result per mutant to `FILE`")
	var checkApplyFlag bool
	flags.BoolVar(&checkApplyFlag, "check-apply", false, "judge nothing: print each selected row whose edit does not apply (ledger, row, owner, why, the commit that last changed its from in its file) and exit 1 if any")
	var checkEditedKillers bool
	var baseRev string
	var dryRun bool
	flags.BoolVar(&checkEditedKillers, "check-edited-killers", false, "anti-assertion-weakening guard: find test/law files edited since -base, map them to affected ledger rows, and re-judge those rows fresh (exit 1 if any survive, fail to apply, or become invalid)")
	flags.StringVar(&baseRev, "base", "", "git base `REV` for -check-edited-killers")
	flags.BoolVar(&dryRun, "dry-run", false, "with -check-edited-killers, list affected mutant rows and exit 0 without judging")
	var anchorDecls bool
	flags.BoolVar(&anchorDecls, "anchor-decls", false, "judge nothing: give each row of every testdata/mutants.json below -root the declaration that holds its edit (decl, MN0), printing what it cannot anchor; with -write, rewrite the ledgers")
	var canonicalKillers bool
	flags.BoolVar(&canonicalKillers, "canonical-killers", false, "judge nothing: list legacy suite IDs in the ledgers below -root; with -write, rewrite them as directory IDs")
	var alpha bool
	var alphaOut, failLogs string
	flags.BoolVar(&alpha, "alpha", false, "judge every killer of each row the packages' tests can kill (the kill matrix, testdata/kills.json) and print one row per test of the packages: class, seconds, unique and total kills, change coupling, failures, ranked as a deletion list (CS-ALPHA)")
	flags.StringVar(&alphaOut, "o", "", "-alpha: write the table to `FILE`")
	flags.StringVar(&failLogs, "fail-logs", "", "-alpha: count each test's failures in the go test logs under `DIR`")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: go run ./scripts/mutants [flags] PACKAGE...   (PACKAGE: check, lower, a package dir or a ledger file)")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if checkEditedKillers {
		var err error
		if cfg.Memory, err = proclimit.ParseSize(memory); err != nil {
			fmt.Fprintf(stderr, "mutants: -memory: %v\n", err)
			return 2
		}
		return runEditedKillers(cfg, baseRev, dryRun, selectRE, ergoFlag, noNativeErgo, flags.Args(), stdout, stderr)
	}
	if dryRun {
		fmt.Fprintln(stderr, "mutants: -dry-run requires -check-edited-killers")
		return 2
	}
	if checkApplyFlag {
		sel, err := regexp.Compile(selectRE)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: -run: %v\n", err)
			return 2
		}
		var ledgers []string
		for _, arg := range flags.Args() {
			ledgers = append(ledgers, ledgerPath(cfg.Root, arg))
		}
		return checkApply(cfg.Root, ledgers, sel.MatchString, stdout, stderr)
	}
	if anchorDecls {
		sel, err := regexp.Compile(selectRE)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: -run: %v\n", err)
			return 2
		}
		return migrateDecls(cfg.Root, sel, write, stdout, stderr)
	}
	if canonicalKillers {
		return migrateKillerIDs(cfg.Root, write, stdout, stderr)
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}
	var err error
	if cfg.Work == "" {
		var done func()
		if cfg.Work, done, err = privateWork(); err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		defer done()
	}
	if cfg.Memory, err = proclimit.ParseSize(memory); err != nil {
		fmt.Fprintf(stderr, "mutants: -memory: %v\n", err)
		return 2
	}
	// reused are the verdicts the store holds (reuse.go), set before any
	// row is judged.
	var reused []Result
	var sel *regexp.Regexp
	if selectRE != "" {
		if sel, err = regexp.Compile(selectRE); err != nil {
			fmt.Fprintf(stderr, "mutants: -run: %v\n", err)
			return 2
		}
	}
	var jobs []Job
	for _, arg := range flags.Args() {
		path := ledgerPath(cfg.Root, arg)
		l, err := readLedger(cfg.Root, path, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		jobs = append(jobs, Job{Path: path, Ledger: l, Select: sel})
		addScope(&cfg, scopeOf(cfg.Root, path))
	}
	// Every .ergo row is formatted and checked before and after its edit,
	// in the run and in -compiles (FK7a, FK12a; source.go).
	cfg.Sources = ergoSources
	if resume {
		if dir, err := os.UserCacheDir(); err == nil {
			cfg.JournalDir = filepath.Join(dir, "ergo-mutants", "journal")
		}
	}
	if jsonOut != "" {
		// Each verdict is written as it lands: a run killed before its end
		// leaves the verdicts it reached.
		cfg.Progress = func(results []Result) {
			if err := writeResults(jsonOut, inLedgerOrder(jobs, append(slices.Clone(reused), results...))); err != nil {
				fmt.Fprintf(stderr, "mutants: %v\n", err)
			}
		}
	}
	if alpha {
		if !fresh {
			if store, err := openCoverStore(); err == nil {
				cfg.CoverStore = store.Dir
			}
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer cancel()
		if err := runAlpha(ctx, cfg, jobs, alphaOut, failLogs, stdout); err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		return 0
	}
	if compileOnly {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer cancel()
		start := time.Now()
		results, err := CompileRows(ctx, cfg, jobs)
		if results == nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		code := report(stdout, jobs, results, &coverage{})
		fmt.Fprintf(stdout, "time: %s\n", time.Since(start).Round(time.Second))
		if jsonOut != "" {
			if werr := writeResults(jsonOut, results); werr != nil {
				fmt.Fprintf(stderr, "mutants: %v\n", werr)
				return 2
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		return code
	}
	if cfg.Cover, cfg.Tools, cfg.Suites, err = suitesFor(cfg.Root, jobs); err != nil {
		fmt.Fprintf(stderr, "mutants: %v\n", err)
		return 2
	}
	if !fresh {
		if store, err := openCoverStore(); err != nil {
			fmt.Fprintf(stderr, "mutants: no coverage segment reused: %v\n", err)
		} else {
			cfg.CoverStore = store.Dir
		}
	}
	if measureOnly {
		if cfg.CoverStore == "" {
			fmt.Fprintln(stderr, "mutants: -measure fills the coverage store, which -fresh turns off")
			return 2
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer cancel()
		c, err := Measure(ctx, cfg, jobs)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "coverage: %d tests of %d suites, stored at %s\n", len(c.Tests), len(cfg.Suites), cfg.CoverStore)
		return 0
	}
	start := time.Now()
	// The rows whose killed verdict the store holds at their key are not
	// judged again (reuse.go); -fresh and -all judge every row.
	judge := jobs
	pass := &reusePass{root: cfg.Root, log: stderr}
	if !fresh && !cfg.All {
		if pass.store, err = openReuseStore(); err != nil {
			fmt.Fprintf(stderr, "mutants: no verdict reused: %v\n", err)
		} else {
			reused, judge = pass.begin(jobs)
		}
	}
	results, cov, runErr := []Result(nil), &coverage{}, error(nil)
	if len(judge) > 0 {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer cancel()
		ergoBin := resolveErgoBinary(cfg.Root, ergoFlag, noNativeErgo)
		var delJobs []Job
		goJudge := judge
		if ergoBin != "" {
			delJobs, goJudge = partitionDelegatedJobs(cfg.Root, cfg, judge)
		}
		var delResults []Result
		if len(delJobs) > 0 {
			var fallback []Job
			delResults, fallback, runErr = runDelegatedErgoJobs(ctx, ergoBin, cfg.Root, fresh, delJobs, stderr)
			if runErr != nil {
				fmt.Fprintf(stderr, "mutants: %v\n", runErr)
				return 2
			}
			if len(fallback) > 0 {
				goJudge = append(goJudge, fallback...)
			}
		}
		var goResults []Result
		if len(goJudge) > 0 {
			if cfg.Cover, cfg.Tools, cfg.Suites, err = suitesFor(cfg.Root, goJudge); err != nil {
				fmt.Fprintf(stderr, "mutants: %v\n", err)
				return 2
			}
			goResults, cov, runErr = Run(ctx, cfg, goJudge)
			if goResults == nil {
				fmt.Fprintf(stderr, "mutants: %v\n", runErr)
				return 2
			}
		}
		results = append(delResults, goResults...)
		if runErr == nil {
			pass.end(judge, results)
		}
	}
	results = inLedgerOrder(jobs, append(reused, results...))
	code := report(stdout, jobs, results, cov)
	fmt.Fprintf(stdout, "time: %s\n", time.Since(start).Round(time.Second))
	if jsonOut != "" {
		if err := writeResults(jsonOut, results); err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "mutants: %v\n", runErr)
		return 2
	}
	if write {
		for _, j := range jobs {
			record(j, results, cov, time.Now())
			if err := writeLedger(j.Path, j.Ledger); err != nil {
				fmt.Fprintf(stderr, "mutants: %v\n", err)
				return 2
			}
		}
	}
	return code
}

// ledgerPath resolves a PACKAGE argument to its ledger file.
func ledgerPath(root, arg string) string {
	switch {
	case strings.HasSuffix(arg, ".json"):
		return arg
	case arg == "check" || arg == "lower":
		return filepath.Join(root, "internal", "sketch", arg, "testdata", "mutants.json")
	}
	return filepath.Join(arg, "testdata", "mutants.json")
}

// scope is what judges the mutants of one ledger: the packages whose
// blocks the coverage pass records, the tools, and the suites it runs.
type scope struct {
	cover  []string
	tools  []Tool
	suites []Suite
}

// scopeOf is the scope of the ledger at path. The checker's and the
// lowering's ledgers take ergoSuites. Any other ledger, at
// DIR/testdata/mutants.json, is its own package's: the coverage pass
// records DIR's blocks over DIR's own tests, the suite named DIR (its
// killers are DIR:Test…). A mutant of such a package's source is in no
// block of check or lower, so under ergoSuites it was uncovered, after a
// coverage pass over every check, lower and std test and every example
// (M1: 21 minutes for runtime/v1's client mutants).
func scopeOf(root, path string) scope {
	absRoot, err1 := filepath.Abs(root)
	dir, err2 := filepath.Abs(filepath.Dir(filepath.Dir(path)))
	rel, err := filepath.Rel(absRoot, dir)
	err = errors.Join(err1, err2, err)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		cover, tools, suites := ergoSuites()
		return scope{cover, tools, suites}
	}
	rel = filepath.ToSlash(rel)
	switch rel {
	case "internal/sketch/check", "internal/sketch/lower":
		cover, tools, suites := ergoSuites()
		return scope{cover, tools, suites}
	}
	return scope{cover: []string{"./" + rel}, suites: []Suite{{Name: rel, Package: "./" + rel}}}
}

// addScope adds s to cfg's packages, tools and suites, once each.
func addScope(cfg *Config, s scope) {
	for _, c := range s.cover {
		if !slices.Contains(cfg.Cover, c) {
			cfg.Cover = append(cfg.Cover, c)
		}
	}
	for _, t := range s.tools {
		if !slices.ContainsFunc(cfg.Tools, func(u Tool) bool { return u.Name == t.Name }) {
			cfg.Tools = append(cfg.Tools, t)
		}
	}
	for _, x := range s.suites {
		if !slices.ContainsFunc(cfg.Suites, func(u Suite) bool { return u.Name == x.Name }) {
			cfg.Suites = append(cfg.Suites, x)
		}
	}
}

// ergoSources validate every .ergo mutant, whichever suites judge it: the
// compiler built from the unmutated checkout formats and checks the
// mutated file's package folder (a std folder checked as a folder has
// diagnostics of its own, such as S108; only new ones count).
var ergoSources = []SourceCheck{{
	Ext:    ".ergo",
	Tool:   Tool{Name: "ergo", Package: "./cmd/ergo", Flags: ergobuild.Flags, Env: []string{ergobuild.Cgo}},
	Format: []string{"{bin:ergo}", "fmt", "--check", "{file}"},
	Check:  []string{"{bin:ergo}", "check", "{dir}"},
}}

// ergoSuites are the suites that judge the checker's and the lowering's
// mutants: their own tests, the std tests (laws and guards), and the
// default example programs, the end-to-end layer.
func ergoSuites() ([]string, []Tool, []Suite) {
	cover := []string{"./internal/sketch/check", "./internal/sketch/lower", "./internal/sketch/api", "./cmd/ergo"}
	tools := []Tool{
		{Name: "ergo", Package: "./cmd/ergo", Flags: ergobuild.Flags, Env: []string{ergobuild.Cgo}},
		{Name: "examples", Package: "./scripts/examples", Flags: []string{"-buildvcs=false"}},
	}
	suites := []Suite{
		{Name: suitename.Check, Package: "./" + suitename.Check},
		// The lowering's tests run the CLI the runner built (main_test.go,
		// cliVariable), not one each test process builds.
		{Name: suitename.Lower, Package: "./" + suitename.Lower, Env: []string{"ERGO_TEST_CLI={bin:ergo}"}},
		{Name: suitename.Std, Package: "./" + suitename.Std},
		{Name: suitename.API, Package: "./" + suitename.API},
		{Name: suitename.Ergo, Package: "./" + suitename.Ergo},
		// The case budget is off: a slow case under the runner's load is not
		// a kill. The per-case timeout stays: a mutant that loops is one.
		{Name: "examples", Command: []string{"{bin:examples}", "-root", "{root}", "-ergo", "{bin:ergo}", "-budget", "1h", "{test}"}, List: defaultExamples},
	}
	return cover, tools, suites
}

// suitesFor chooses the suites of the ledgers to run. The checker's and
// the lowering's ledgers take ergoSuites. The ledger of any other package
// (runtime/v1, batch 14) is judged by that package's own tests, with
// coverage of the packages its mutants edit (a runtime/v1 test kills a
// mutant of runtime/rt too).
func suitesFor(root string, jobs []Job) ([]string, []Tool, []Suite, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, nil, err
	}
	var cover []string
	var tools []Tool
	var suites []Suite
	addSuite := func(s Suite) {
		if i := slices.IndexFunc(suites, func(added Suite) bool { return added.Name == s.Name }); i >= 0 {
			suites[i].Env = append(suites[i].Env, s.Env...)
			return
		}
		suites = append(suites, s)
	}
	add := func(pkg string) {
		// A package of a nested module is no package of the checkout's
		// module: -coverpkg cannot name it.
		if nestedModule(absRoot, strings.TrimPrefix(pkg, "./")) != "" {
			return
		}
		if !slices.Contains(cover, pkg) {
			cover = append(cover, pkg)
		}
	}
	for _, j := range jobs {
		abs, err := filepath.Abs(j.Path)
		if err != nil {
			return nil, nil, nil, err
		}
		rel, err := filepath.Rel(absRoot, filepath.Dir(filepath.Dir(abs)))
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, nil, nil, fmt.Errorf("ledger %s is outside the checkout %s", j.Path, root)
		}
		rel = filepath.ToSlash(rel)
		if rel == suitename.Check || rel == suitename.Lower {
			if len(j.Ledger.Env) > 0 {
				return nil, nil, nil, fmt.Errorf("ledger %s: env is for a package ledger's own suite; check and lower share the ergo suites", j.Path)
			}
			if tools == nil {
				c, t, s := ergoSuites()
				tools = t
				for _, suite := range s {
					addSuite(suite)
				}
				for _, p := range c {
					add(p)
				}
			}
			continue
		}
		// Only a Go package is covered, and only a package with tests is a
		// suite: an example's ledger (examples/fetchpool) edits .ergo
		// files and is judged by the killers it names alone.
		if goFiles(absRoot, rel, false) {
			add("./" + rel)
		}
		for _, m := range j.Ledger.Mutants {
			if dir := filepath.ToSlash(filepath.Dir(m.File)); goFiles(absRoot, dir, false) {
				add("./" + dir)
			}
		}
		if goFiles(absRoot, rel, true) {
			// The ledger's env reaches every run of its own suite (the
			// coverage pass and the mutant pass), and enters the tree key
			// with it.
			own := Suite{Name: rel, Package: "./" + rel, Env: slices.Clone(j.Ledger.Env)}
			// A package of a module nested in the checkout (hosts/mygo) is
			// built and run in its own module, as namedSuite builds a
			// killer there (MUT-MODULES).
			if mod := nestedModule(absRoot, rel); mod != "" {
				own.Module, own.Package = mod, "./"+strings.TrimPrefix(strings.TrimPrefix(rel, mod), "/")
			}
			addSuite(own)
		}
	}
	// A killer or anchor test in a suite no ledger brought is run too, and
	// only the tests the ledgers name: its other tests judge nothing here,
	// and some need a harness of their own (runtime/v1's ergoexplore tests
	// hang without the explored worker). No row is left to run by hand.
	// An id whose suite or test does not exist is an error, not a killer
	// left out: before, v1:, sched: and rt-explore: killers were skipped
	// and their rows judged without them (MUT-names). Only the selected
	// rows bring suites: a run of one row builds and runs the killers that
	// row names, not every killer of its ledger.
	// A -run selection judges its rows by the killers they name: each
	// suite runs only those tests, not every test of its package (TM:
	// one irspike row started a pass over its 531 tests, for hours), and a
	// suite none of them names is not built.
	selection := !slices.ContainsFunc(jobs, func(j Job) bool { return j.Select == nil })
	if selection {
		for i := range suites {
			if suites[i].Only == nil {
				suites[i].Only = []string{}
			}
		}
	}
	var unresolved []error
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			if j.Select != nil && !j.Select.MatchString(m.Name) {
				continue
			}
			for _, id := range killerIDs(m) {
				name, test := splitID(id)
				i := slices.IndexFunc(suites, func(s Suite) bool { return s.Name == name })
				if i < 0 && name == commandSuite {
					s, need, err := commandsOf(jobs)
					if err != nil {
						return nil, nil, nil, err
					}
					for _, t := range need {
						if !slices.ContainsFunc(tools, func(u Tool) bool { return u.Name == t.Name }) {
							tools = append(tools, t)
						}
					}
					suites = append(suites, s)
					i = len(suites) - 1
				}
				if i < 0 {
					s, need, ok := namedSuite(absRoot, name)
					if !ok {
						unresolved = append(unresolved, fmt.Errorf("%s: mutant %q: %q names no suite (an ergo suite, a module directory or DIR[TAGS])", j.Path, m.Name, id))
						continue
					}
					for _, t := range need {
						if !slices.ContainsFunc(tools, func(u Tool) bool { return u.Name == t.Name }) {
							tools = append(tools, t)
						}
					}
					s.Only = []string{}
					suites = append(suites, s)
					i = len(suites) - 1
				}
				found, family := hasTest(absRoot, &suites[i], test)
				if !found {
					unresolved = append(unresolved, fmt.Errorf("%s: mutant %q: %q names no test of suite %s", j.Path, m.Name, id, name))
					continue
				}
				if family && !slices.Contains(suites[i].Families, test) {
					suites[i].Families = append(suites[i].Families, test)
				}
				if suites[i].Only != nil && !slices.Contains(suites[i].Only, test) {
					suites[i].Only = append(suites[i].Only, test)
				}
				if !slices.Contains(suites[i].Named, test) {
					suites[i].Named = append(suites[i].Named, test)
				}
			}
		}
	}
	if len(unresolved) > 0 {
		return nil, nil, nil, errors.Join(unresolved...)
	}
	if selection {
		suites = slices.DeleteFunc(suites, func(s Suite) bool { return len(s.Only) == 0 })
	}
	return cover, tools, suites, nil
}

// commandsOf is the suite of the jobs' ledgers' commands, with the tools
// their words name (one of ergoSuites' tools). Two ledgers may share a
// command's name only with the same words.
func commandsOf(jobs []Job) (Suite, []Tool, error) {
	s := Suite{Name: commandSuite, Commands: map[string][]string{}, Only: []string{}}
	for _, j := range jobs {
		for name, words := range j.Ledger.Commands {
			if had, ok := s.Commands[name]; ok && !slices.Equal(had, words) {
				return Suite{}, nil, fmt.Errorf("%s: command %q differs from another ledger's", j.Path, name)
			}
			s.Commands[name] = words
		}
	}
	_, known, _ := ergoSuites()
	var need []Tool
	for _, name := range toolsOf(&s) {
		i := slices.IndexFunc(known, func(t Tool) bool { return t.Name == name })
		if i < 0 {
			return Suite{}, nil, fmt.Errorf("a command names the tool %q, which is none of %v", name, toolNames(known))
		}
		need = append(need, known[i])
	}
	return s, need, nil
}

// ergoTestsTool runs the Ergo tests of a package at check time
// (scripts/ergotests, ST-0).
var ergoTestsTool = Tool{Name: "ergotests", Package: "./scripts/ergotests", Flags: []string{"-buildvcs=false"}}

// ergoTool is the ergo CLI, as ergoSuites builds it.
var ergoTool = Tool{Name: "ergo", Package: "./cmd/ergo", Flags: ergobuild.Flags, Env: []string{ergobuild.Cgo}}

// ergoPackage says whether dir is an Ergo package with no Go test of its
// own (compiler/syntax): a killer DIR:Name there is the Ergo test Name,
// run by ergotests (ST-0). A directory with Go tests stays a Go suite.
func ergoPackage(root, dir string) bool {
	files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.ergo"))
	return len(files) > 0 && !goFiles(root, dir, true)
}

// ergoTest says whether dir declares the Ergo test named test, or for
// law:L the law L, top-level, a member law T.L, or pkg.T.L an imported operation type's law its providers run (LAW-KILLER).
func ergoTest(root, dir, test string) bool {
	if law, ok := strings.CutPrefix(test, "law:"); ok {
		// law:L, law:T.L, or law:pkg.T.L: a member law an imported
		// operation type declares, which dir's providers run.
		return lawledger.LawDeclared(root, dir, law)
	}
	files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.ergo"))
	decl := regexp.MustCompile(`(?m)^test ` + regexp.QuoteMeta(test) + `\s*\{`)
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil && decl.Match(data) {
			return true
		}
	}
	return false
}

// goFiles says whether the module directory dir holds a Go file: a test
// file with tests, a non-test one without.
func goFiles(root, dir string, tests bool) bool {
	files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
	return slices.ContainsFunc(files, func(f string) bool { return strings.HasSuffix(f, "_test.go") == tests })
}

// namedSuite resolves the suite of a killer id that no ledger's scope
// brought: a suite of ergoSuites by its directory or command name,
// with the tools it runs; or a package of the module by its directory,
// DIR or DIR[TAGS] (built with -tags=TAGS).
func namedSuite(root, name string) (Suite, []Tool, bool) {
	_, tools, suites := ergoSuites()
	for _, s := range suites {
		if s.Name == name {
			var need []Tool
			for _, t := range tools {
				if slices.Contains(toolsOf(&s), t.Name) {
					need = append(need, t)
				}
			}
			return s, need, true
		}
	}
	dir, tags, tagged := strings.Cut(name, "[")
	if tagged {
		if !strings.HasSuffix(tags, "]") {
			return Suite{}, nil, false
		}
		tags = strings.TrimSuffix(tags, "]")
	}
	if dir == "" || strings.HasPrefix(dir, ".") || filepath.IsAbs(dir) {
		return Suite{}, nil, false
	}
	if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !info.IsDir() {
		return Suite{}, nil, false
	}
	if !tagged && ergoPackage(root, dir) {
		// ergo decides a law check time leaves undecided (LAW-KILLER).
		return Suite{Name: name, Command: []string{"{bin:ergotests}", "-ergo", "{bin:ergo}", "-killer", "{test}", "{root}/" + dir}}, []Tool{ergoTestsTool, ergoTool}, true
	}
	if mod := nestedModule(root, dir); mod != "" {
		return Suite{Name: name, Module: mod, Package: "./" + strings.TrimPrefix(strings.TrimPrefix(dir, mod), "/"), Tags: tags}, nil, true
	}
	return Suite{Name: name, Package: "./" + dir, Tags: tags}, nil, true
}

// nestedModule is the directory of the innermost Go module below root that
// holds dir (dir itself or a parent with a go.mod), or "" when dir is in
// the checkout's own module. go test from the checkout's root refuses such
// a package ("main module does not contain package").
func nestedModule(root, dir string) string {
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d), "go.mod")); err == nil {
			return d
		}
	}
	return ""
}

// hasTest reports whether test is a test of suite s in the checkout at
// root: a top-level func of a _test.go file in a Go suite's directory, or
// an example program of the examples suite. A Go suite's runner lists its
// binary's tests and keeps only those Only names, so a name that matches
// none ran nothing. family is set when test is no func but the family of
// its parts, TestPart0 … (Suite.Families).
func hasTest(root string, s *Suite, test string) (found, family bool) {
	switch {
	case s.Commands != nil:
		_, ok := s.Commands[test]
		return ok, false
	case s.Package != "":
		// A nested module's suite names its package in its module.
		files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(s.Module), filepath.FromSlash(s.Package), "*_test.go"))
		one := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(test) + `\(`)
		part := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(test) + `Part[0-9]+\(`)
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil && one.Match(data) {
				return true, false
			}
		}
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil && part.Match(data) {
				return true, true
			}
		}
		return false, false
	case len(s.Command) > 0 && s.Command[0] == "{bin:ergotests}":
		return ergoTest(root, s.Name, test), false
	case s.Name == "examples":
		dir := filepath.Join(root, "examples", test)
		if mod, ok := strings.CutPrefix(test, "modules/"); ok {
			dir, test = filepath.Join(root, "modules", mod), mod // a module of modules/ (MK0)
		}
		info, err := os.Stat(dir)
		return test != "" && !strings.ContainsAny(test, `/\`) && err == nil && info.IsDir(), false
	}
	return test != "", false
}

// defaultExamples lists the example programs tagged default.
func defaultExamples(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "examples"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		tags, err := os.ReadFile(filepath.Join(root, "examples", e.Name(), "tags"))
		if e.IsDir() && err == nil && slices.Contains(strings.Fields(string(tags)), "default") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// report prints the verdicts and the lists a reader acts on, and returns
// the exit code.
func report(w io.Writer, jobs []Job, results []Result, cov *coverage) int {
	byName := map[string]*Mutant{}
	for _, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			byName[j.Path+"\x00"+m.Name] = m
		}
	}
	counts := map[string]int{}
	var newSurvivors, gaps, closed, moved, only, owners, unjudged, hung []string
	invalid, ran, most, stopped := 0, 0, 0, 0
	for _, r := range results {
		if r.Status == "" {
			continue // not judged: the run was interrupted
		}
		m := byName[r.Ledger+"\x00"+r.Name]
		counts[r.Status]++
		where := fmt.Sprintf("%s:%d", m.File, r.Line)
		line := fmt.Sprintf("%-9s %s  %s", r.Status, r.Name, where)
		if r.Killer != "" {
			line += fmt.Sprintf("  by %s (%s)", r.Killer, r.Reason)
		}
		fmt.Fprintf(w, "%s  [%d run, %d stopped, %d covering, %.0f s]\n", line, len(r.Ran), len(r.Stopped), r.Covering, r.Seconds)
		ran += len(r.Ran)
		most = max(most, len(r.Ran))
		stopped += len(r.Stopped)
		switch r.Status {
		case Survived, Uncovered:
			if m.Gap == "" {
				newSurvivors = append(newSurvivors, fmt.Sprintf("%s (%s, %s; known killers: %s)", r.Name, where, r.Status, strings.Join(m.Killers, " ")))
			} else {
				gaps = append(gaps, fmt.Sprintf("%s (%s): %s", r.Name, where, m.Gap))
			}
		case Killed:
			if m.Gap != "" {
				closed = append(closed, fmt.Sprintf("%s: killed by %s; the ledger says: %s", r.Name, r.Killer, m.Gap))
			}
			known := killerIDs(&Mutant{Killers: m.Killers})
			if len(known) > 0 && !slices.Contains(known, r.Killer) &&
				!slices.ContainsFunc(r.Killers, func(id string) bool { return slices.Contains(known, id) }) {
				moved = append(moved, fmt.Sprintf("%s: killed by %s, not by its known killers %s", r.Name, r.Killer, strings.Join(m.Killers, " ")))
			}
			if len(r.Killers) == 1 && len(r.Ran) == r.Covering {
				only = append(only, fmt.Sprintf("%s: killed only by %s", r.Name, r.Killer))
			}
		case Stale, Invalid:
			owners = append(owners, fmt.Sprintf("%s (%s, origin %s): %s", r.Name, r.Status, m.Origin, firstLine(r.Detail)))
			if r.Status == Invalid {
				invalid++
			}
		case Unjudged:
			unjudged = append(unjudged, fmt.Sprintf("%s (%s): %s", r.Name, where, r.Detail))
		case Hung:
			hung = append(hung, fmt.Sprintf("%s (%s, origin %s): %s", r.Name, where, m.Origin, firstLine(r.Detail)))
		}
	}
	var keys []string
	for k := range counts {
		keys = append(keys, fmt.Sprintf("%d %s", counts[k], k))
	}
	slices.Sort(keys)
	fmt.Fprintf(w, "mutants: %d judged: %s\n", len(results), strings.Join(keys, ", "))
	fmt.Fprintf(w, "receipt: %d tests run on mutants (at most %d on one), %d stopped by a first kill\n", ran, most, stopped)
	section(w, "NEW SURVIVORS (no test kills them and the ledger records no gap)", newSurvivors)
	section(w, "known gaps (survivors the ledger records)", gaps)
	section(w, "gaps closed (now killed: clear the gap)", closed)
	section(w, "killer moved", moved)
	section(w, "killed only by", only)
	section(w, "report to the origin (stale: the edit no longer applies; invalid: the mutated tree does not build, or the mutated file does not compile; an invalid row fails the run)", owners)
	var broken []string
	for _, t := range cov.Tests {
		if t.Failed {
			broken = append(broken, t.ID+": "+firstLine(t.Detail))
		}
	}
	section(w, "tests that fail on the unmutated tree (they kill nothing)", broken)
	section(w, "NOT JUDGED (a test that covers them fails on the unmutated tree: fix it, then run again)", unjudged)
	section(w, "HUNG (no test killed them, and a test ran into its timeout: a deadline only stops a hung test, it kills nothing; fix the row or the test; a hung row fails the run)", hung)
	if len(newSurvivors) > 0 || len(unjudged) > 0 || len(hung) > 0 || invalid > 0 {
		return 1
	}
	return 0
}

func section(w io.Writer, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	for _, l := range lines {
		fmt.Fprintf(w, "  %s\n", l)
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// record writes what the run found into the ledger: a killed mutant's
// killers (those found first, then the earlier ones that still exist) and
// no gap; a survivor that the ledger had not judged yet gets a gap. A
// survivor with known killers stays as it is: that is a loss to fix.
func record(j Job, results []Result, cov *coverage, now time.Time) {
	for _, r := range results {
		if r.Ledger != j.Path {
			continue
		}
		for _, m := range j.Ledger.Mutants {
			if m.Name != r.Name {
				continue
			}
			switch r.Status {
			case Killed:
				killers := make([]string, 0, len(r.Killers))
				for _, id := range r.Killers {
					killers = append(killers, suitename.ForLedger(id))
				}
				for _, k := range m.Killers {
					suite, name := splitID(k)
					if cov.Lookup(suite+":"+name) != nil && !slices.Contains(killers, k) && !slices.Contains(killers, suitename.ForLedger(suite+":"+name)) {
						killers = append(killers, k)
					}
				}
				m.Killers, m.Gap = killers, ""
			case Survived, Uncovered:
				if len(m.Killers) == 0 && m.Gap == "" {
					m.Gap = fmt.Sprintf("%s: no test kills it (runner, %s)", r.Status, now.Format("2006-01-02"))
				}
			}
		}
	}
}

// writeResults writes the judged results to path, whole or not at all:
// it is rewritten after each verdict, and a run may die at any point.
func writeResults(path string, results []Result) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".results-*")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	for _, r := range results {
		if r.Status == "" {
			continue // not judged yet
		}
		if err := encoder.Encode(r); err != nil {
			f.Close()
			os.Remove(f.Name())
			return err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}
