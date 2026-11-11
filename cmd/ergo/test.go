package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// testCommand runs a source's tests: `ergo test FILE [--seed N] [--states N]
// [--law NAME] [--skip-law NAME] [--receipt FILE]
// [--explore [dfs|pct|random] [--preempt B] [--depth D] [--schedules S]]
// [--schedule TOKEN] [-work] [--target go]`. --states is the
// budget of states of each reachability law's search (machine laws;
// 1,000,000 by default). --law runs only the law NAME and no test;
// --skip-law runs all but that law, with a skip line. --explore links the same test
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
func testCommand(args []string) error {
	opts, err := parseFileOptions("test", args)
	if err != nil {
		return invalidInvocation(err)
	}
	if recursivePattern(opts.file) {
		return recursiveTestCommand(opts)
	}
	return testPackage(opts)
}

func testPackage(opts fileOptions) error {
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, opts)
	}
	return testLoadedPackage(opts, l)
}

func testLoadedPackage(opts fileOptions, l loaded) error {
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
	tc := toolchain(opts)
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
		return exploreSearch(os.Stdout, l, binary, childArgs, opts.search, seed, limit)
	}
	child := testProgramCommand(binary, childArgs, limit)
	return runEntryProcess(child, limit, proclimit.Watch(child))
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
