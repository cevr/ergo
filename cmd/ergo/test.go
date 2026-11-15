package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/runtime/rt"
	"ergo.local/prototype/runtime/rt/rttrace"
	v1 "ergo.local/prototype/runtime/v1"
)

// testCommand runs a source's tests: `ergo test FILE [--seed N] [--states N]
// [--law NAME] [--skip-law NAME] [--run NAME] [--receipt FILE]
// [--explore [dfs|pct|random] [--preempt B] [--depth D] [--schedules S]]
// [--schedule TOKEN] [-work] [--target go]`. --states is the
// budget of states of each reachability law's search (machine laws;
// 1,000,000 by default). --law runs only the law NAME and no test;
// --skip-law runs all but that law, with a skip line. --run runs the one
// test or law of that name (a law by the title its report prints), plain,
// explored or replayed; a name the package does not have is a usage error
// of the test binary (exit 2). --explore links the same test
// module with the ergoexplore tag, so its tasks run on one baton under the
// cooperative schedule, and a deadlock is a defect that names the tasks
// (runtime.md, Explored schedules). With a mode, the tests run under many
// schedules in worker processes of the one binary, and a failure prints
// its schedule (explore.go); --schedule replays one. It is a transport over sketchfront's test build
// (build.ModeTest, lower.LowerTests): it checks the source, builds the test
// binary into a private temporary directory, never the working directory,
// dist/ or the module, and runs it as a child with --seed, through the
// same child path as run (runEntryProcess), so SIGINT gives 130 and
// SIGTERM 143. Its exit status is the runner's: 0 when every test passes,
// 1 when one fails, 2 when a defect stops the run. An invalid invocation, a
// malformed --seed included, is a usage error (exit 2) that runs nothing
// and prints no report line; a check, lowering or Go build failure prints
// its diagnostics and exits 1 with no PASS line. -work prints WORK=DIR on
// stderr, DIR being the test module in the emission cache.
//
// --trace prints, after the run, the trace of every scene script the tests
// played: one line per act, in the order the acts ran, each the line std
// ui's PlayTrace returns for it (testTrace). It takes one package, played
// plainly: with a pattern or --explore it is a usage error.
func testCommand(args []string) error {
	args, trace := traceFlag(args)
	opts, err := parseFileOptions("test", args)
	if err != nil {
		return invalidInvocation(err)
	}
	if trace && (recursivePattern(opts.file) || opts.explore || opts.search.mode != "") {
		return invalidInvocation(fmt.Errorf("ergo test: --trace takes one package, played plainly, not a pattern or --explore"))
	}
	if recursivePattern(opts.file) {
		return recursiveTestCommand(opts)
	}
	return testPackage(opts, trace)
}

// traceFlag removes test's --trace from args, and reports whether it was
// there. A --trace that is an option's value (--law --trace) stays that
// option's.
func traceFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	trace := false
	for i, a := range args {
		if a == "--trace" && (i == 0 || !takesValue(args[i-1])) {
			trace = true
			continue
		}
		out = append(out, a)
	}
	return out, trace
}

// takesValue reports whether option a reads the next word as its value.
func takesValue(a string) bool {
	switch a {
	case "--target", "-o", "--seed", "--states", "--receipt", "--law", "--skip-law", "--platform", "--eval-steps", "--eval-bytes", "--run", "--preempt", "--depth", "--schedules", "--schedule":
		return true
	}
	return false
}

func testPackage(opts fileOptions, trace bool) error {
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, opts)
	}
	return testLoadedTraced(opts, l, trace)
}

func testLoadedPackage(opts fileOptions, l loaded) error {
	return testLoadedTraced(opts, l, false)
}

// testLoadedTraced is testLoadedPackage, with --trace when trace is set.
func testLoadedTraced(opts fileOptions, l loaded, trace bool) error {
	dir, err := os.MkdirTemp("", "ergo-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, l.name+".test")
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	tc, err := buildAction(opts)
	if err != nil {
		return err
	}
	if opts.explore {
		// The tag chooses the runtime's controlled scheduler at link
		// time; the emitted module is the one a plain `ergo test` uses
		// (runtime.md, Explored schedules).
		tc.Tags = []string{"ergoexplore"}
	}
	module, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeTest,
		Source:    l.source.Admitted(),
		Output:    binary,
		Cache:     build.Cache{Root: root},
		Toolchain: tc,
		Stderr:    os.Stderr,
	})
	if opts.work && module.Dir != "" {
		fmt.Fprintf(os.Stderr, "WORK=%s\n", module.Dir)
	}
	if err != nil {
		return err
	}
	// The binary names the source, as it was given here (a file or a
	// package's folder), in a refuted case's or law's replay line (X1b),
	// and a receipt names the source and its revision (L10).
	childArgs := []string{"--source", opts.file, "--revision", l.result.Revision}
	if opts.seed != "" {
		childArgs = append(childArgs, "--seed", opts.seed)
	}
	if opts.states != "" {
		childArgs = append(childArgs, "--states", opts.states)
	}
	if opts.receipt != "" {
		path, err := filepath.Abs(opts.receipt)
		if err != nil {
			return err
		}
		childArgs = append(childArgs, "--receipt", path)
	}
	if opts.law != "" {
		childArgs = append(childArgs, "--law", opts.law)
	}
	if opts.skipLaw != "" {
		childArgs = append(childArgs, "--skip-law", opts.skipLaw)
	}
	if opts.run != "" {
		// The test binary selects the one test or law (lower's $Seed and
		// $Want), plain or explored: one name, one reading of it.
		childArgs = append(childArgs, "--run", opts.run)
	}
	// The test binary runs under a memory limit, 2 GiB unless
	// ERGO_PROGRAM_MEMORY sets another, and its stderr passes through a
	// watcher, so a test that grows without bound stops early and `ergo`
	// says which limit stopped it (docs/tooling.md, "Test memory").
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return err
	}
	if opts.search.mode != "" {
		// A search runs each schedule in a worker of the binary, each
		// under the same limit (explore.go).
		seed := uint64(1)
		if opts.seed != "" {
			seed, _ = strconv.ParseUint(opts.seed, 10, 64)
		}
		return exploreSearch(os.Stdout, l, binary, childArgs, opts.search, opts.run, seed, limit)
	}
	child := testProgramCommand(binary, childArgs, limit)
	tracePath := ""
	if trace {
		// The test binary's root records its trace into this file when
		// main ends (runtime/v1 TraceEnv), whatever the verdict.
		tracePath = filepath.Join(dir, "trace.jsonl")
		child.Env = append(child.Env, v1.TraceEnv+"="+tracePath)
	}
	ran := runEntryProcess(child, limit, proclimit.Watch(child))
	if trace {
		if err := printSteps(os.Stdout, tracePath); err != nil {
			return err
		}
	}
	if ran != nil {
		return ran
	}
	if opts.law != "" || opts.skipLaw != "" || opts.run != "" || opts.explore {
		// A partial run checks only some laws: it leaves the ledger.
		return nil
	}
	// The passing run writes the law ledger (runtime.md, Tests 10): the
	// laws and site instances the run build uses, checked alone.
	changed, err := sketchfront.UpdateLedger(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    l.source.Admitted(),
		Cache:     build.Cache{Root: root},
		Toolchain: tc, // no explore tag: an explored run returned above
		Stderr:    os.Stderr,
		Ledger:    ledgerPath(opts.file),
	})
	if err != nil {
		return err
	}
	if changed > 0 {
		fmt.Fprintf(os.Stderr, "ergo.laws: %d entries changed\n", changed)
	}
	return nil
}

// printSteps writes the step events of the trace at path to w: a line
// "trace: N steps", then each step's line, in the order the runtime raised
// them. A trace whose recorder dropped events says so on stderr, as a
// step may be among them. A run that wrote no trace (it stopped before
// main ended) prints no step.
func printSteps(w io.Writer, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(w, "trace: no trace was written")
		return nil
	}
	if err != nil {
		return err
	}
	head, rest, _ := bytes.Cut(data, []byte("\n"))
	var h rttrace.Header
	if err := json.Unmarshal(head, &h); err != nil {
		return fmt.Errorf("ergo test --trace: %s: %w", path, err)
	}
	var steps []string
	for _, line := range bytes.Split(rest, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var l rttrace.Line
		if err := json.Unmarshal(line, &l); err != nil {
			return fmt.Errorf("ergo test --trace: %s: %w", path, err)
		}
		if l.Kind == rt.TraceStep.String() {
			steps = append(steps, l.Step)
		}
	}
	fmt.Fprintf(w, "trace: %d steps\n", len(steps))
	for _, s := range steps {
		fmt.Fprintln(w, s)
	}
	if h.Dropped > 0 {
		fmt.Fprintf(os.Stderr, "ergo test --trace: the recorder dropped %d events, so steps may be missing; run one test with --run\n", h.Dropped)
	}
	return nil
}

// testProgramCommand keeps the test program's collector inside its hard
// data limit. Build tools may use a larger heap and GOGC, but passing those
// settings to a smaller bounded program can reach ENOMEM before collection.
// Three quarters leaves headroom for stacks, metadata and non-heap data;
// the normal GC target also avoids an inherited off or high GOGC. An
// explicitly unbounded program retains the caller's collector settings.
func testProgramCommand(binary string, args []string, limit uint64) *exec.Cmd {
	child := exec.Command(binary, args...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	child.Env = boundedProgramEnv(limit)
	return child
}

// boundedProgramEnv is the collector policy of every test-program child,
// plain or explored. An explicit zero keeps the caller's settings.
func boundedProgramEnv(limit uint64) []string {
	env := os.Environ()
	if limit != 0 {
		env = nil
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GOGC=") && !strings.HasPrefix(entry, "GOMEMLIMIT=") {
				env = append(env, entry)
			}
		}
		env = append(env, "GOGC=100", "GOMEMLIMIT="+strconv.FormatUint(limit/4*3, 10))
	}
	return env
}

// validSeed reports whether s is a seed the test binary reads: a decimal
// uint64 with no sign.
func validSeed(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// validLaw reports whether s may name a law for --law or --skip-law: empty
// (not given), or a word that is not an option and holds no space.
func validLaw(s string) bool {
	return !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\n")
}

// validStates reports whether s is a budget the test binary reads: a
// positive decimal int with no sign.
func validStates(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && s[0] != '+'
}
