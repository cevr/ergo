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
// coverage (once per tree: the result is kept under -work): every
// top-level test of the check, lower and std packages and every default
// example program, each in its own process, with coverage of check and
// lower (the ergo CLI and the examples runner built with coverage, their
// main packages included). Then, for each mutant, it applies the edit,
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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mutants", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cfg := Config{Log: stderr}
	var selectRE, memory, jsonOut string
	var write bool
	flags.StringVar(&cfg.Root, "root", ".", "the checkout")
	flags.StringVar(&cfg.Work, "work", filepath.Join(os.TempDir(), "ergo-mutants"), "scratch `DIR`: the copies, the binaries and the kept coverage")
	flags.StringVar(&selectRE, "run", "", "judge only the mutants whose name matches `REGEXP`")
	flags.BoolVar(&cfg.All, "all", false, "run every covering test of each mutant, to name every killer")
	flags.BoolVar(&write, "write", false, "record the killers found (and the gaps of mutants judged for the first time) in the ledgers")
	flags.IntVar(&cfg.Workers, "workers", 2, "mutants judged at once, each in its own copy")
	flags.IntVar(&cfg.Procs, "procs", 2, "GOMAXPROCS and go -p of each child")
	flags.StringVar(&memory, "memory", "4GiB", "RLIMIT_DATA of each child (KiB, MiB, GiB, or 0 for none)")
	flags.StringVar(&cfg.Coverage, "coverage", "", "read the coverage from `FILE` instead of measuring it")
	flags.DurationVar(&cfg.CoverDeadline, "cover-deadline", time.Hour, "deadline of each child of the coverage pass")
	flags.StringVar(&jsonOut, "json", "", "write one JSON result per mutant to `FILE`")
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
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}
	var err error
	if cfg.Memory, err = proclimit.ParseSize(memory); err != nil {
		fmt.Fprintf(stderr, "mutants: -memory: %v\n", err)
		return 2
	}
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
		l, err := readLedger(path)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		jobs = append(jobs, Job{Path: path, Ledger: l, Select: sel})
	}
	if cfg.Cover, cfg.Tools, cfg.Suites, err = suitesFor(cfg.Root, jobs); err != nil {
		fmt.Fprintf(stderr, "mutants: %v\n", err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	start := time.Now()
	results, cov, runErr := Run(ctx, cfg, jobs)
	if results == nil {
		fmt.Fprintf(stderr, "mutants: %v\n", runErr)
		return 2
	}
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

// ergoSuites are the suites that judge the checker's and the lowering's
// mutants: their own tests, the std tests (laws and guards), and the
// default example programs, the end-to-end layer.
func ergoSuites() ([]string, []Tool, []Suite) {
	cover := []string{"./internal/sketch/check", "./internal/sketch/lower"}
	tools := []Tool{
		{Name: "ergo", Package: "./cmd/ergo", Flags: []string{"-buildvcs=false"}},
		{Name: "examples", Package: "./scripts/examples", Flags: []string{"-buildvcs=false"}},
	}
	suites := []Suite{
		{Name: "check", Package: "./internal/sketch/check"},
		// The lowering's tests run the CLI the runner built (main_test.go,
		// cliVariable), not one each test process builds.
		{Name: "lower", Package: "./internal/sketch/lower", Env: []string{"ERGO_TEST_CLI={bin:ergo}"}},
		{Name: "std", Package: "./internal/sketch/std"},
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
	add := func(pkg string) {
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
		if rel == "internal/sketch/check" || rel == "internal/sketch/lower" {
			if tools == nil {
				c, t, s := ergoSuites()
				tools, suites = t, append(suites, s...)
				for _, p := range c {
					add(p)
				}
			}
			continue
		}
		add("./" + rel)
		for _, m := range j.Ledger.Mutants {
			add("./" + filepath.ToSlash(filepath.Dir(m.File)))
		}
		suites = append(suites, Suite{Name: rel, Package: "./" + rel})
	}
	return cover, tools, suites, nil
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
	var newSurvivors, gaps, closed, moved, only, owners []string
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
		fmt.Fprintf(w, "%s  [%d run, %d covering, %.0f s]\n", line, len(r.Ran), r.Covering, r.Seconds)
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
			if len(m.Killers) > 0 && !slices.Contains(m.Killers, r.Killer) {
				moved = append(moved, fmt.Sprintf("%s: killed by %s, not by its known killers %s", r.Name, r.Killer, strings.Join(m.Killers, " ")))
			}
			if len(r.Killers) == 1 && len(r.Ran) == r.Covering {
				only = append(only, fmt.Sprintf("%s: killed only by %s", r.Name, r.Killer))
			}
		case Stale, Invalid:
			owners = append(owners, fmt.Sprintf("%s (%s, origin %s): %s", r.Name, r.Status, m.Origin, firstLine(r.Detail)))
		}
	}
	var keys []string
	for k := range counts {
		keys = append(keys, fmt.Sprintf("%d %s", counts[k], k))
	}
	slices.Sort(keys)
	fmt.Fprintf(w, "mutants: %d judged: %s\n", len(results), strings.Join(keys, ", "))
	section(w, "NEW SURVIVORS (no test kills them and the ledger records no gap)", newSurvivors)
	section(w, "known gaps (survivors the ledger records)", gaps)
	section(w, "gaps closed (now killed: clear the gap)", closed)
	section(w, "killer moved", moved)
	section(w, "killed only by", only)
	section(w, "report to the origin (stale: the edit no longer applies; invalid: the mutated tree does not build)", owners)
	var broken []string
	for _, t := range cov.Tests {
		if t.Failed {
			broken = append(broken, t.ID+": "+firstLine(t.Detail))
		}
	}
	section(w, "tests that fail on the unmutated tree (they kill nothing)", broken)
	if len(newSurvivors) > 0 {
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
				killers := slices.Clone(r.Killers)
				for _, k := range m.Killers {
					if cov.test(k) != nil && !slices.Contains(killers, k) {
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

func writeResults(path string, results []Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	for _, r := range results {
		if err := encoder.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}
